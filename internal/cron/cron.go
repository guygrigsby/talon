// Package cron is talon-gateway's embedded cron scheduler. It owns
// the in-memory job set, persists state to ~/.talon/cron/jobs.json,
// and fires due jobs by dispatching back through a caller-supplied
// DispatchFunc — typically the gateway's RPC registry, so any
// session-agnostic registry method can be scheduled without
// teaching the cron service how to deliver to channels, agents, etc.
//
// Out-of-process delivery (webhooks, channels, message tools) is
// not modeled here. A user who wants those can register a registry
// method that performs the delivery and schedule that method.
//
// Design notes:
//   - Cron expressions are standard 5-field (minute, hour, dom, month,
//     dow) plus the @hourly/@daily/@weekly/@monthly/@yearly descriptors.
//     Seconds-resolution (6-field) is supported when the expression
//     starts with "0-59" or otherwise validates under the seconds-aware
//     parser. Both behaviors come from robfig/cron/v3 — we just route
//     to the right parser based on field count.
//   - Persistence: jobs.json holds the entire job set (one file, atomic
//     tmp+rename on every write). Run-history is a separate JSONL file
//     so query/append paths don't fight each other. Both live under
//     ~/.talon/cron/ so the user's overlay holds the source of truth.
//   - Scheduling: a single goroutine ticks once a second, walks jobs,
//     fires any whose nextRunMs <= now. Per-job per-tick is fine at
//     the scale of a personal gateway (dozens of jobs, not thousands).
//     Tests inject a `now` func and drive Tick directly to avoid
//     wall-clock races.
package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	robcron "github.com/robfig/cron/v3"
)

// DispatchFunc is the seam back to the caller's RPC dispatcher. Cron
// fires due jobs by calling this with the job's action method + params.
// The returned `any` and `error` are recorded in the run log; a
// non-nil error marks the run failed but does not disable the job.
//
// Implementations should be safe to call concurrently — the scheduler
// fires jobs sequentially in the tick loop today, but tests and
// follow-up parallelism may change that.
type DispatchFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Action is what a job does when it fires. v1 supports a single shape:
// invoke a registry method with the supplied params. Future shapes
// (shell, webhook, agent.turn) would add tagged variants.
type Action struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Job is one scheduled entry. Times are unix-millis for round-tripping
// through JS callers without precision loss; the Go side converts to
// time.Time internally as needed.
//
// LastStatus is a free-form string ("ok", "error", "skipped"); LastErr
// carries the rendered error message when LastStatus="error". A
// disabled job's nextRunMs is left frozen so re-enabling restores its
// schedule without waiting for the next eligible boundary.
type Job struct {
	ID          string `json:"id"`
	Expression  string `json:"expression"`
	Action      Action `json:"action"`
	Enabled     bool   `json:"enabled"`
	NextRunMs   int64  `json:"nextRunMs,omitempty"`
	LastRunMs   int64  `json:"lastRunMs,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastErr     string `json:"lastErr,omitempty"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// Run is one entry in the run log. The cron service appends one Run
// per fire (manual or scheduled). RunID is unique across all jobs so
// the log can be queried without per-job partitioning.
type Run struct {
	RunID       string `json:"runId"`
	JobID       string `json:"jobId"`
	Method      string `json:"method"`
	StartedAtMs int64  `json:"startedAtMs"`
	DurationMs  int64  `json:"durationMs"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	// Manual is true when the run was triggered via Service.RunNow
	// (cron.run RPC) rather than fired by the scheduler tick.
	Manual bool `json:"manual,omitempty"`
}

// Service is the scheduler runtime. Construct with New, persist+start
// with Start, stop with Stop. Add/Remove/RunNow are safe to call
// concurrently from RPC handlers.
type Service struct {
	store    *Store
	runs     *RunLog
	dispatch DispatchFunc
	now      func() time.Time

	mu   sync.Mutex
	jobs map[string]*Job

	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a Service backed by store + runs. dispatch is the
// callback the scheduler invokes when a job fires; nil disables firing
// (the scheduler still ticks and computes nextRun, but actions never
// execute — useful for tests that only assert on persistence).
//
// `now` defaults to time.Now when nil; tests inject a fixed clock.
func New(store *Store, runs *RunLog, dispatch DispatchFunc, now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, errors.New("cron: store is required")
	}
	if runs == nil {
		return nil, errors.New("cron: runs is required")
	}
	if now == nil {
		now = time.Now
	}
	jobs, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("cron: load store: %w", err)
	}
	byID := make(map[string]*Job, len(jobs))
	for i := range jobs {
		j := jobs[i]
		byID[j.ID] = &j
	}
	s := &Service{
		store:    store,
		runs:     runs,
		dispatch: dispatch,
		now:      now,
		jobs:     byID,
	}
	// On boot, recompute next-run for every enabled job: the gateway
	// may have been down through one or more eligible fires and we
	// don't want to replay missed runs (would spam if down for hours).
	// Standard cron semantics: a missed slot is missed.
	for _, j := range s.jobs {
		if !j.Enabled {
			continue
		}
		if next, err := nextAfter(j.Expression, s.now()); err == nil {
			j.NextRunMs = next.UnixMilli()
		} else {
			slog.Warn("cron: invalid expression on load",
				"id", j.ID, "expression", j.Expression, "err", err)
		}
	}
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("cron: persist on boot: %w", err)
	}
	return s, nil
}

// Start launches the tick goroutine. ctx cancellation stops the
// scheduler; Stop is the explicit alternative. tickInterval should be
// 1 * time.Second for production; tests can pass a longer value (or
// drive Tick directly without calling Start at all).
func (s *Service) Start(ctx context.Context, tickInterval time.Duration) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Tick(ctx)
			}
		}
	}()
}

// Stop cancels the tick goroutine and waits for it to exit. Idempotent.
func (s *Service) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.cancel = nil
	if s.done != nil {
		<-s.done
	}
}

// Tick advances the scheduler by one cycle: any enabled job whose
// nextRunMs <= now is fired, has its lastRun/Status updated, and gets
// its nextRunMs recomputed. Exposed so tests can step the scheduler
// without driving wall-clock.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	due := s.snapshotDue(now)
	for _, j := range due {
		s.fire(ctx, j, false, now)
	}
	if len(due) > 0 {
		if err := s.persist(); err != nil {
			slog.Error("cron: persist after fire", "err", err)
		}
	}
}

// snapshotDue copies pointers to enabled, due jobs out of s.jobs under
// lock so the fire loop runs without holding s.mu (dispatch may take a
// while; we don't want to block Add/Remove during it).
func (s *Service) snapshotDue(now time.Time) []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowMs := now.UnixMilli()
	var due []*Job
	for _, j := range s.jobs {
		if !j.Enabled {
			continue
		}
		if j.NextRunMs == 0 || j.NextRunMs > nowMs {
			continue
		}
		due = append(due, j)
	}
	return due
}

// fire dispatches one job, records the run, and recomputes nextRun.
// manual indicates whether this is a cron.run trigger (recorded in
// the run log so the user can tell scheduled from forced).
func (s *Service) fire(ctx context.Context, j *Job, manual bool, now time.Time) {
	startedAt := now
	run := Run{
		RunID:       newRunID(now),
		JobID:       j.ID,
		Method:      j.Action.Method,
		StartedAtMs: startedAt.UnixMilli(),
		Manual:      manual,
	}
	if s.dispatch == nil {
		run.OK = false
		run.Error = "cron: no dispatch func wired"
	} else {
		_, err := s.dispatch(ctx, j.Action.Method, j.Action.Params)
		end := s.now()
		run.DurationMs = end.Sub(startedAt).Milliseconds()
		if err != nil {
			run.OK = false
			run.Error = err.Error()
		} else {
			run.OK = true
		}
	}
	if run.DurationMs == 0 {
		run.DurationMs = s.now().Sub(startedAt).Milliseconds()
	}
	if err := s.runs.Append(run); err != nil {
		slog.Error("cron: append run log", "id", j.ID, "err", err)
	}

	// Update job state under lock + recompute next.
	s.mu.Lock()
	defer s.mu.Unlock()
	j.LastRunMs = run.StartedAtMs
	if run.OK {
		j.LastStatus = "ok"
		j.LastErr = ""
	} else {
		j.LastStatus = "error"
		j.LastErr = run.Error
	}
	if next, err := nextAfter(j.Expression, s.now()); err == nil {
		j.NextRunMs = next.UnixMilli()
	}
}

// Add inserts or replaces a job. Returns the resulting Job (with
// CreatedAtMs/NextRunMs populated). Validates the expression up front
// so a malformed cron is rejected at the call site rather than logged
// later as a tick failure.
func (s *Service) Add(j Job) (Job, error) {
	if strings.TrimSpace(j.ID) == "" {
		return Job{}, errors.New("cron: job id is required")
	}
	if strings.TrimSpace(j.Action.Method) == "" {
		return Job{}, errors.New("cron: action.method is required")
	}
	next, err := nextAfter(j.Expression, s.now())
	if err != nil {
		return Job{}, fmt.Errorf("cron: invalid expression: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.jobs[j.ID]; ok {
		// Replace preserves CreatedAtMs from the prior incarnation so
		// "edit a job" doesn't reset its provenance.
		j.CreatedAtMs = existing.CreatedAtMs
	} else {
		j.CreatedAtMs = s.now().UnixMilli()
	}
	if j.Enabled {
		j.NextRunMs = next.UnixMilli()
	}
	s.jobs[j.ID] = &j
	if err := s.persistLocked(); err != nil {
		return Job{}, fmt.Errorf("cron: persist: %w", err)
	}
	return j, nil
}

// Remove deletes a job by id. Returns false when the id is unknown
// (caller can decide whether to surface that as 404 or 200).
func (s *Service) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return false
	}
	delete(s.jobs, id)
	if err := s.persistLocked(); err != nil {
		slog.Error("cron: persist after remove", "id", id, "err", err)
	}
	return true
}

// Get returns a snapshot of the named job, or false when unknown.
func (s *Service) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns a stable-ordered snapshot of all jobs. Sorting by ID
// keeps the cron.list response deterministic for the UI/CLI.
func (s *Service) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	// Insertion-time order isn't stable across reload (map iteration);
	// sort by id for predictability.
	sortJobsByID(out)
	return out
}

// RunNow fires a job immediately, ignoring its schedule. Returns the
// run record so RPC callers can echo it back. The recorded Run carries
// Manual=true so users can distinguish forced runs from scheduled
// fires when reading the log.
func (s *Service) RunNow(ctx context.Context, id string) (Run, error) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return Run{}, fmt.Errorf("cron: unknown job id %q", id)
	}
	now := s.now()
	s.fire(ctx, j, true, now)
	if err := s.persist(); err != nil {
		slog.Error("cron: persist after manual run", "id", id, "err", err)
	}
	// Read the latest run for this job back out of the log.
	last, err := s.runs.Latest(id)
	if err != nil {
		return Run{}, fmt.Errorf("cron: read run after fire: %w", err)
	}
	return last, nil
}

// Runs returns up to limit recent runs, optionally filtered by jobID.
// limit<=0 means "all"; capped to 1000 internally so the response
// stays bounded.
func (s *Service) Runs(jobID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	return s.runs.Read(jobID, limit)
}

func (s *Service) persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked()
}

func (s *Service) persistLocked() error {
	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, *j)
	}
	sortJobsByID(jobs)
	return s.store.Save(jobs)
}

// nextAfter parses expr and returns the next fire time strictly after
// `from`. Field-count detection picks between the seconds-aware and
// classic 5-field parsers.
func nextAfter(expr string, from time.Time) (time.Time, error) {
	parser, err := selectParser(expr)
	if err != nil {
		return time.Time{}, err
	}
	sched, err := parser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

// selectParser returns the right robfig/cron parser for expr. We
// support the descriptor shorthand (@hourly etc.) plus 5-field
// classic and 6-field with seconds.
func selectParser(expr string) (robcron.Parser, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "@") {
		// Descriptors are accepted by both parsers; pick the classic
		// one for consistency.
		return robcron.NewParser(robcron.Minute | robcron.Hour | robcron.Dom | robcron.Month | robcron.Dow | robcron.Descriptor), nil
	}
	// Robfig's 5-field parser rejects 6-field input and vice versa, so
	// route by field count.
	fields := strings.Fields(expr)
	switch len(fields) {
	case 5:
		return robcron.NewParser(robcron.Minute | robcron.Hour | robcron.Dom | robcron.Month | robcron.Dow), nil
	case 6:
		return robcron.NewParser(robcron.Second | robcron.Minute | robcron.Hour | robcron.Dom | robcron.Month | robcron.Dow), nil
	default:
		return robcron.Parser{}, fmt.Errorf("expected 5 or 6 fields, got %d", len(fields))
	}
}

func sortJobsByID(jobs []Job) {
	// inline sort.Slice equivalent without importing "sort" twice.
	for i := 1; i < len(jobs); i++ {
		for j := i; j > 0 && jobs[j-1].ID > jobs[j].ID; j-- {
			jobs[j-1], jobs[j] = jobs[j], jobs[j-1]
		}
	}
}

// newRunID returns a sortable, unique run id: "<unixms>-<rand4>". The
// timestamp prefix lets callers eyeball ordering without an extra
// sort. rand4 disambiguates fires that share a millisecond (manual
// stress test, deterministic clock in tests).
func newRunID(now time.Time) string {
	return fmt.Sprintf("%d-%s", now.UnixMilli(), randHex4())
}
