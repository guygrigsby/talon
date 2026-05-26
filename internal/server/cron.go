package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	cronpkg "github.com/guygrigsby/talon/internal/cron"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// CronHandler exposes the talon-gateway's cron scheduler over the WS
// RPC surface: cron.list, cron.add, cron.remove, cron.run, cron.status,
// cron.runs.
//
// The handler holds a *cron.Service whose dispatch func points back at
// this server's Registry — so a scheduled job can fire any
// session-agnostic registry method (chat.send is NOT session-agnostic
// today; users wanting "send a chat at 9am" should wrap it in a
// dedicated handler that constructs the session context).
//
// Lifecycle: NewCronHandler attaches the service to s.registry; the
// caller must invoke Start(ctx) before serving traffic and Stop() on
// shutdown to drain the tick goroutine cleanly.
type CronHandler struct {
	svc *cronpkg.Service
}

// NewCronHandler constructs a handler bound to paths. The job store
// lives at <talon>/cron/jobs.json and the run log at
// <talon>/cron/runs.jsonl.
//
// dispatch is the seam the scheduler uses to fire jobs; it should
// dispatch through the server's Registry. New() returns an error if
// the on-disk job store is unreadable (a corrupt jobs.json should
// surface at boot, not silently disable cron).
func NewCronHandler(paths talonpath.Paths, dispatch cronpkg.DispatchFunc) (*CronHandler, error) {
	store := cronpkg.NewStore(filepath.Join(paths.Talon.Dir, "cron", "jobs.json"))
	runs := cronpkg.NewRunLog(filepath.Join(paths.Talon.Dir, "cron", "runs.jsonl"))
	svc, err := cronpkg.New(store, runs, dispatch, nil)
	if err != nil {
		return nil, err
	}
	return &CronHandler{svc: svc}, nil
}

// Service exposes the underlying scheduler so callers (Server.Run)
// can drive Start/Stop. Tests also reach in here for direct Tick
// control.
func (h *CronHandler) Service() *cronpkg.Service { return h.svc }

// Register wires the cron.* RPCs into r.
func (h *CronHandler) Register(r *Registry) {
	r.Register("cron.list", h.handleList)
	r.Register("cron.add", h.handleAdd)
	r.Register("cron.remove", h.handleRemove)
	r.Register("cron.run", h.handleRun)
	r.Register("cron.status", h.handleStatus)
	r.Register("cron.show", h.handleShow)
	r.Register("cron.enable", h.handleEnable)
	r.Register("cron.disable", h.handleDisable)
	r.Register("cron.runs", h.handleRuns)
}

// --- cron.list ------------------------------------------------------------

type cronListParams struct {
	All bool `json:"all,omitempty"`
}

func (h *CronHandler) handleList(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronListParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	jobs := h.svc.List()
	if !p.All {
		enabled := jobs[:0]
		for _, j := range jobs {
			if j.Enabled {
				enabled = append(enabled, j)
			}
		}
		jobs = enabled
	}
	return map[string]any{"jobs": jobs}, nil
}

// --- cron.add -------------------------------------------------------------

type cronAddParams struct {
	ID         string          `json:"id"`
	Expression string          `json:"expression"`
	Action     cronpkg.Action  `json:"action"`
	Enabled    *bool           `json:"enabled,omitempty"`
	// Params at top level is accepted as a convenience: equivalent to
	// action.params. Lets callers write {id, expression, method, params}
	// without nesting. Only honored when Action.Method is empty.
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (h *CronHandler) handleAdd(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronAddParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.add: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.add: id is required"}
	}
	if strings.TrimSpace(p.Expression) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.add: expression is required"}
	}
	action := p.Action
	if action.Method == "" && p.Method != "" {
		action.Method = p.Method
		action.Params = p.Params
	}
	if action.Method == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.add: action.method is required"}
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	job := cronpkg.Job{
		ID:         p.ID,
		Expression: p.Expression,
		Action:     action,
		Enabled:    enabled,
	}
	out, err := h.svc.Add(job)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.add: " + err.Error()}
	}
	return map[string]any{"job": out}, nil
}

// --- cron.remove ----------------------------------------------------------

type cronRemoveParams struct {
	ID string `json:"id"`
}

func (h *CronHandler) handleRemove(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronRemoveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.remove: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.remove: id is required"}
	}
	removed := h.svc.Remove(p.ID)
	return map[string]any{"ok": true, "removed": removed}, nil
}

// --- cron.run -------------------------------------------------------------

type cronRunParams struct {
	ID string `json:"id"`
}

func (h *CronHandler) handleRun(ctx context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.run: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.run: id is required"}
	}
	run, err := h.svc.RunNow(ctx, p.ID)
	if err != nil {
		// Distinguish "unknown id" from "internal error" so the CLI can
		// render the right message.
		if isUnknownJob(err) {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.run: " + err.Error()}
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "cron.run: " + err.Error()}
	}
	return map[string]any{"run": run}, nil
}

// --- cron.status ----------------------------------------------------------
// Returns scheduler-level metadata (running, job counts, next fire).
// Use cron.show to inspect a specific job by id.

func (h *CronHandler) handleStatus(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	return h.svc.Status(), nil
}

// --- cron.show ------------------------------------------------------------

type cronShowParams struct {
	ID string `json:"id"`
}

func (h *CronHandler) handleShow(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronShowParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.show: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.show: id is required"}
	}
	j, ok := h.svc.Get(p.ID)
	if !ok {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: fmt.Sprintf("cron.show: unknown job id %q", p.ID)}
	}
	return map[string]any{"job": j}, nil
}

// --- cron.enable / cron.disable -------------------------------------------

type cronEnableParams struct {
	ID string `json:"id"`
}

func (h *CronHandler) handleEnable(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronEnableParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.enable: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.enable: id is required"}
	}
	j, err := h.svc.SetEnabled(p.ID, true)
	if err != nil {
		if isUnknownJob(err) {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.enable: " + err.Error()}
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "cron.enable: " + err.Error()}
	}
	return map[string]any{"job": j}, nil
}

func (h *CronHandler) handleDisable(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronEnableParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.disable: " + err.Error()}
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.disable: id is required"}
	}
	j, err := h.svc.SetEnabled(p.ID, false)
	if err != nil {
		if isUnknownJob(err) {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.disable: " + err.Error()}
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "cron.disable: " + err.Error()}
	}
	return map[string]any{"job": j}, nil
}

// --- cron.runs ------------------------------------------------------------

type cronRunsParams struct {
	ID    string `json:"id,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (h *CronHandler) handleRuns(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p cronRunsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "cron.runs: " + err.Error()}
		}
	}
	runs, err := h.svc.Runs(p.ID, p.Limit)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "cron.runs: " + err.Error()}
	}
	return map[string]any{"runs": runs}, nil
}

// isUnknownJob detects the cron.Service's "unknown job id" sentinel
// without leaking package-internal error types. Matches the prefix
// used in cron.go's RunNow.
func isUnknownJob(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown job id")
}

// dispatchFromRegistry wraps a Registry into a cronpkg.DispatchFunc.
// Errors from the registry come back as FrameError; this adapter
// flattens them into a Go error so the run log records the message.
// Nil session is intentional — cron-fired runs have no client.
func dispatchFromRegistry(r *Registry) cronpkg.DispatchFunc {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		out, ferr := r.Dispatch(ctx, HandlerCtx{}, method, params)
		if ferr != nil {
			return nil, errors.New(ferr.Code + ": " + ferr.Message)
		}
		return out, nil
	}
}
