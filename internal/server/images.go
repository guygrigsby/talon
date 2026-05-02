package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/comfyui"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/netutil"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
)

// ImagesHandler serves images.generate (async) and images.fetch (sync).
//
// Generate flow: read the user-exported workflow JSON, patch the
// configured prompt/negative/seed nodes, open a /ws subscription
// against ComfyUI under a fresh clientID, submit the workflow, stream
// the resulting frames, then on execution_success pull the
// /history/<id> record to discover output filenames. The handler
// returns runId synchronously; events flow over the talon WS as
// "images" events with the same runId. Fetch is a synchronous
// pass-through that base64-encodes the bytes from /view so the openclaw
// UI can render via data: URIs without a second HTTP hop.
//
// Workflow source-of-truth lives on disk (default
// ~/.talon/images/comfyui-default.json). Talon never authors a
// workflow — the user exports an "API Format" workflow from ComfyUI
// and tells talon which node ids to patch via
// images.providers.comfyui.workflow.{promptNodeId,
// negativePromptNodeId, seedNodeId}. That keeps ComfyUI's editor as
// the source of truth for models, samplers, dimensions, LoRAs.
type ImagesHandler struct {
	paths openclaw.Paths

	// dial returns a fresh ComfyUI client given a base URL. Default
	// wraps comfyui.New; tests inject a stub to avoid network IO.
	dial func(baseURL string) ComfyUIClient

	// emit is the seam for events. Default pushes to the talon
	// session via PushEvent; tests swap to capture events in memory.
	// Returns the underlying PushEvent error so callers can give up
	// when the client disconnects mid-run.
	emit func(sess *Session, runID, sessionKey, state string, data map[string]any) error

	// runsMu / runs deduplicate concurrent submissions sharing the
	// same idempotency key (sessionKey + runId), matching the chat
	// handler's contract.
	runsMu sync.Mutex
	runs   map[string]string
}

// ComfyUIClient is the surface ImagesHandler needs from comfyui.Client.
// Defined here as an interface so tests can substitute without touching
// the prod constructor. comfyui.Client satisfies this through Go's
// structural typing.
type ComfyUIClient interface {
	Submit(ctx context.Context, workflow json.RawMessage, clientID string) (*comfyui.SubmitResult, error)
	Events(ctx context.Context, clientID string) (<-chan comfyui.Event, <-chan error, error)
	History(ctx context.Context, promptID string) (*comfyui.HistoryEntry, error)
	HistoryAll(ctx context.Context, max int) ([]comfyui.HistoryListEntry, error)
	Fetch(ctx context.Context, ref comfyui.ImageRef, preview string) ([]byte, string, error)
}

// NewImagesHandler returns a handler bound to paths. The default dial
// uses comfyui.New and the default emit pushes through the session.
// Tests can swap either via WithDial / WithEmit.
func NewImagesHandler(paths openclaw.Paths) *ImagesHandler {
	h := &ImagesHandler{
		paths: paths,
		dial:  func(u string) ComfyUIClient { return comfyui.New(u) },
		runs:  map[string]string{},
	}
	h.emit = h.defaultEmit
	return h
}

// WithDial replaces the default comfyui dialer; intended for tests.
func (h *ImagesHandler) WithDial(d func(string) ComfyUIClient) *ImagesHandler {
	h.dial = d
	return h
}

// WithEmit replaces the default event emitter; intended for tests.
func (h *ImagesHandler) WithEmit(e func(sess *Session, runID, sessionKey, state string, data map[string]any) error) *ImagesHandler {
	h.emit = e
	return h
}

// Register wires the images.* RPCs into r.
func (h *ImagesHandler) Register(r *Registry) {
	r.Register("images.generate", h.handleGenerate)
	r.Register("images.fetch", h.handleFetch)
	r.Register("images.list", h.handleList)
	r.Register("images.delete", h.handleDelete)
	r.Register("images.workflows.list", h.handleWorkflowsList)
}

// --- images.generate -------------------------------------------------------

type imagesGenerateParams struct {
	SessionKey     string `json:"sessionKey"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negativePrompt"`
	Seed           *int64 `json:"seed,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
	// WorkflowID picks one of the builtin workflows (e.g.
	// "dixar-character"). Empty falls back to the user's
	// config-driven workflow at images.providers.comfyui.workflow.path.
	WorkflowID string `json:"workflowId,omitempty"`
}

func (h *ImagesHandler) handleGenerate(_ context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesGenerateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.generate: " + err.Error()}
	}
	if strings.TrimSpace(p.SessionKey) == "" || strings.TrimSpace(p.Prompt) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.generate: sessionKey and prompt are required"}
	}

	cfg, ferr := h.loadComfyUIConfig(p.WorkflowID)
	if ferr != nil {
		return nil, ferr
	}

	workflow, ferr := readAndPatchWorkflow(cfg, p)
	if ferr != nil {
		return nil, ferr
	}

	runID := p.IdempotencyKey
	if runID == "" {
		fresh, err := newImagesRunID()
		if err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: "images.generate: " + err.Error()}
		}
		runID = fresh
	}
	runKey := p.SessionKey + "|" + runID
	h.runsMu.Lock()
	if _, dup := h.runs[runKey]; dup {
		h.runsMu.Unlock()
		return map[string]any{"runId": runID}, nil
	}
	h.runs[runKey] = runID
	h.runsMu.Unlock()

	go h.runGenerate(hc.Session, runID, p.SessionKey, cfg, workflow, runKey)
	return map[string]any{"runId": runID}, nil
}

// runGenerate is the async background goroutine that submits the
// workflow, listens to ComfyUI's /ws stream, and emits images events
// back through the talon session. The flow mirrors chat.runStream but
// the event surface is distinct.
func (h *ImagesHandler) runGenerate(sess *Session, runID, sessionKey string, cfg comfyUIConfig, workflow json.RawMessage, runKey string) {
	defer func() {
		h.runsMu.Lock()
		delete(h.runs, runKey)
		h.runsMu.Unlock()
	}()

	// emitFailures counts consecutive PushEvent failures so we stop
	// spamming the gateway log when the client has disconnected. The
	// run itself keeps going to completion regardless — persistence
	// (index append) and ComfyUI accounting (queue drain) shouldn't
	// depend on whether anyone's listening.
	emitFailures := 0
	emitDead := false
	const emitFailureThreshold = 3
	emit := func(state string, data map[string]any) bool {
		if emitDead {
			return true
		}
		if err := h.emit(sess, runID, sessionKey, state, data); err != nil {
			emitFailures++
			if emitFailures >= emitFailureThreshold {
				emitDead = true
			}
			return true
		}
		emitFailures = 0
		return true
	}

	cli := h.dial(cfg.BaseURL)
	clientID, err := newImagesRunID()
	if err != nil {
		emit("error", map[string]any{"errorMessage": err.Error()})
		return
	}

	// Open the WS BEFORE submitting so we don't miss "executing" /
	// "progress" frames on small/cached jobs. ComfyUI replays nothing
	// — anything emitted before the listener is attached is lost.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	events, errs, err := cli.Events(ctx, clientID)
	if err != nil {
		emit("error", map[string]any{"errorMessage": "comfyui events: " + err.Error()})
		return
	}

	submit, err := cli.Submit(ctx, workflow, clientID)
	if err != nil {
		emit("error", map[string]any{"errorMessage": "comfyui submit: " + err.Error()})
		return
	}
	if len(submit.NodeErrors) > 0 {
		emit("error", map[string]any{
			"errorMessage": "comfyui rejected workflow",
			"nodeErrors":   submit.NodeErrors,
		})
		return
	}
	if !emit("queued", map[string]any{
		"promptId":      submit.PromptID,
		"queuePosition": submit.Number,
	}) {
		return // client gone
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				emit("error", map[string]any{"errorMessage": "comfyui event stream closed unexpectedly"})
				return
			}
			done, ok := h.dispatchEvent(emit, cli, submit.PromptID, ev)
			if done || !ok {
				return
			}
		case e := <-errs:
			if e == nil {
				continue
			}
			emit("error", map[string]any{"errorMessage": e.Error()})
			return
		case <-ctx.Done():
			emit("error", map[string]any{"errorMessage": "images.generate timed out"})
			return
		}
	}
}

// dispatchEvent processes one /ws frame using the supplied emit
// closure (which short-circuits on repeated PushEvent failures).
// Returns (done, ok): done=true when the run reached a terminal state,
// ok=false when the emitter has given up because the client disconnected.
func (h *ImagesHandler) dispatchEvent(emit func(state string, data map[string]any) bool, cli ComfyUIClient, promptID string, ev comfyui.Event) (done, ok bool) {
	// Filter by prompt_id when present — clientIDs are unique per
	// submission today but ComfyUI's protocol allows interleaving.
	if pid := promptIDFromData(ev.Data); pid != "" && pid != promptID {
		return false, true
	}
	switch ev.Type {
	case "executing":
		// A nil node means the run finished; non-nil indicates which
		// node is currently running. Pass the node id through so the
		// UI can show a hint about the active step.
		var d struct {
			Node any `json:"node"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		return false, emit("running", map[string]any{"node": d.Node})
	case "progress":
		var d struct {
			Value int `json:"value"`
			Max   int `json:"max"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		return false, emit("progress", map[string]any{"value": d.Value, "max": d.Max})
	case "execution_error":
		// ComfyUI's execution_error event carries the actual failure
		// detail — node id, node type (e.g. CheckpointLoaderSimple),
		// and exception_message (the human-readable reason, like
		// "ckpt_name 'foo.safetensors' not found in [...]"). Surface
		// those in errorMessage so the user sees what's broken
		// instead of the useless "comfyui execution_error" string.
		emit("error", map[string]any{
			"errorMessage": formatExecutionError(ev.Data),
			"detail":       json.RawMessage(ev.Data),
		})
		return true, true
	case "execution_interrupted":
		emit("error", map[string]any{"errorMessage": "comfyui execution_interrupted"})
		return true, true
	case "execution_success":
		// Workflow finished — pull /history to discover output images.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entry, err := cli.History(ctx, promptID)
		if err != nil || entry == nil {
			msg := "comfyui history not available"
			if err != nil {
				msg = err.Error()
			}
			emit("error", map[string]any{"errorMessage": msg})
			return true, true
		}
		var refs []comfyui.ImageRef
		for _, out := range entry.Outputs {
			refs = append(refs, out.Images...)
		}
		// Persist to the on-disk index so the gallery survives
		// ComfyUI restarts. Best-effort: a write failure shouldn't
		// fail the run — the user already has the image.
		nowMs := time.Now().UnixMilli()
		newItems := make([]imagesListItem, 0, len(refs))
		for _, ref := range refs {
			newItems = append(newItems, imagesListItem{
				Filename:    ref.Filename,
				Subfolder:   ref.Subfolder,
				Type:        ref.Type,
				PromptID:    promptID,
				CreatedAtMs: nowMs,
			})
		}
		_ = h.appendToImagesIndex(newItems)
		emit("final", map[string]any{
			"promptId": promptID,
			"images":   refs,
		})
		return true, true
	}
	return false, true
}

// promptIDFromData returns the prompt_id field nested in ComfyUI's
// data envelope, or "" when absent (e.g. status frames).
func promptIDFromData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var d struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return ""
	}
	return d.PromptID
}

// imagesEventPayload is the openclaw-style envelope the talon UI will
// listen for on `images` events. seq is reserved for future ordering;
// today every emit increments lastSeq.
type imagesEventPayload struct {
	RunID      string         `json:"runId"`
	SessionKey string         `json:"sessionKey"`
	State      string         `json:"state"`
	Data       map[string]any `json:"data,omitempty"`
}

func (h *ImagesHandler) defaultEmit(sess *Session, runID, sessionKey, state string, data map[string]any) error {
	if sess == nil {
		return nil // synchronous test paths use HandlerCtx{} with no session
	}
	payload := imagesEventPayload{RunID: runID, SessionKey: sessionKey, State: state, Data: data}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sess.PushEvent(ctx, "images", payload)
}

// --- images.fetch ----------------------------------------------------------

type imagesFetchParams struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
	// Preview, when non-empty, is forwarded to ComfyUI's /view as
	// preview=<value> — typically "webp;quality=70" for thumbnail
	// renders. Falls back to full-resolution PNG when omitted.
	Preview string `json:"preview"`
}

func (h *ImagesHandler) handleFetch(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesFetchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.fetch: " + err.Error()}
	}
	if p.Filename == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.fetch: filename is required"}
	}
	// images.fetch only needs the base URL; pass "" for workflowID.
	cfg, ferr := h.loadComfyUIConfig("")
	if ferr != nil {
		return nil, ferr
	}
	cli := h.dial(cfg.BaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, ctype, err := cli.Fetch(ctx, comfyui.ImageRef{
		Filename: p.Filename, Subfolder: p.Subfolder, Type: p.Type,
	}, p.Preview)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.fetch: " + err.Error()}
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return map[string]any{
		"filename":    p.Filename,
		"contentType": ctype,
		"size":        len(body),
		"base64":      base64.StdEncoding.EncodeToString(body),
		"dataUrl":     "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(body),
	}, nil
}

// --- images.list -----------------------------------------------------------

type imagesListParams struct {
	Limit int `json:"limit"`
}

// imagesListItem is the per-image envelope the gallery view consumes.
// CreatedAtMs orders the list newest-first when present; index entries
// always carry a timestamp, history-only entries fall back to 0 (which
// the UI sorts last).
type imagesListItem struct {
	Filename    string `json:"filename"`
	Subfolder   string `json:"subfolder"`
	Type        string `json:"type"`
	PromptID    string `json:"promptId"`
	CreatedAtMs int64  `json:"createdAtMs,omitempty"`
}

// imagesIndexFile is the on-disk image index. Appended-to on every
// successful generation; read on every images.list call. Lets the
// gallery survive ComfyUI restarts (which clear its in-memory
// /history). The file is JSON-array shape for simplicity; a JSONL
// append-only log would scale better but we expect <10k entries on a
// personal install for the foreseeable future.
type imagesIndexFile struct {
	Items []imagesListItem `json:"items"`
}

const imagesIndexFilename = "index.json"

// imagesIndexPath returns the absolute path to the index file. Lives
// in ~/.talon/images/ next to the user's exported workflow.
func (h *ImagesHandler) imagesIndexPath() string {
	return filepath.Join(h.paths.Talon.Dir, "images", imagesIndexFilename)
}

func (h *ImagesHandler) readImagesIndex() ([]imagesListItem, error) {
	raw, err := os.ReadFile(h.imagesIndexPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var idx imagesIndexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, err
	}
	return idx.Items, nil
}

// appendToImagesIndex persists items to the index, deduping by filename
// so re-runs of the same prompt-id (or repeated final events) don't
// double-write. mkdir is best-effort — if it fails we fall through to
// WriteFile which surfaces the real error.
func (h *ImagesHandler) appendToImagesIndex(newItems []imagesListItem) error {
	if len(newItems) == 0 {
		return nil
	}
	dir := filepath.Dir(h.imagesIndexPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	existing, _ := h.readImagesIndex() // nil on missing/corrupt; we'll rewrite
	seen := make(map[string]struct{}, len(existing)+len(newItems))
	merged := make([]imagesListItem, 0, len(existing)+len(newItems))
	// Newest first: prepend new items, then existing in original order.
	for _, it := range newItems {
		key := it.Subfolder + "|" + it.Filename
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, it)
	}
	for _, it := range existing {
		key := it.Subfolder + "|" + it.Filename
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, it)
	}
	body, err := json.MarshalIndent(imagesIndexFile{Items: merged}, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.imagesIndexPath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.imagesIndexPath())
}

func (h *ImagesHandler) handleList(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.list: " + err.Error()}
		}
	}
	if p.Limit <= 0 || p.Limit > 500 {
		// 50 is a sensible default for a thumbnail grid; >500 risks
		// huge payloads even if the UI immediately discards excess.
		p.Limit = 50
	}
	// images.list only needs the base URL; pass "" for workflowID.
	cfg, ferr := h.loadComfyUIConfig("")
	if ferr != nil {
		return nil, ferr
	}

	// Live history first — newer generations may not be in the index
	// yet (e.g. handler restarted between generate and finalize).
	cli := h.dial(cfg.BaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := cli.HistoryAll(ctx, p.Limit*2)
	if err != nil {
		// Don't fail the whole list — the persistent index might still
		// have entries the user wants to see. Log and continue.
		entries = nil
	}
	live := make([]imagesListItem, 0)
	for _, e := range entries {
		for _, out := range e.Entry.Outputs {
			for _, ref := range out.Images {
				live = append(live, imagesListItem{
					Filename:  ref.Filename,
					Subfolder: ref.Subfolder,
					Type:      ref.Type,
					PromptID:  e.PromptID,
				})
			}
		}
	}

	// Persistent index: survives ComfyUI restarts.
	indexed, _ := h.readImagesIndex() // nil on first run

	// Merge: index is the source of truth for createdAtMs; live entries
	// fill in anything the index hasn't seen yet (e.g. images
	// generated before this feature shipped).
	seen := map[string]int{} // key → position in merged
	merged := make([]imagesListItem, 0, len(indexed)+len(live))
	for _, it := range indexed {
		key := it.Subfolder + "|" + it.Filename
		seen[key] = len(merged)
		merged = append(merged, it)
	}
	for _, it := range live {
		key := it.Subfolder + "|" + it.Filename
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, it)
	}

	// Newest first: index entries are written newest-first by
	// appendToImagesIndex; sorting would require timestamps on every
	// row. Live-only entries (no createdAtMs) stay at the end.
	if len(merged) > p.Limit {
		merged = merged[:p.Limit]
	}
	return map[string]any{"images": merged}, nil
}

// --- images.delete ---------------------------------------------------------

type imagesDeleteParams struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
}

// handleDelete removes an image from talon's index. The underlying file
// on ComfyUI's disk is left in place — ComfyUI doesn't expose a delete
// endpoint and we don't have filesystem access to its output directory
// from this side of the LAN. The image disappears from the gallery
// because images.list reads the index; users wanting on-disk cleanup
// can prune ComfyUI's output dir themselves.
func (h *ImagesHandler) handleDelete(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.delete: " + err.Error()}
	}
	if p.Filename == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.delete: filename is required"}
	}
	existing, err := h.readImagesIndex()
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.delete: read index: " + err.Error()}
	}
	matchKey := p.Subfolder + "|" + p.Filename
	kept := make([]imagesListItem, 0, len(existing))
	removed := 0
	for _, it := range existing {
		if (it.Subfolder + "|" + it.Filename) == matchKey {
			removed++
			continue
		}
		kept = append(kept, it)
	}
	if removed == 0 {
		// Nothing in the index — still report ok so the UI can drop
		// the row optimistically without a special "not in index"
		// branch. ComfyUI history items will reappear on the next
		// list, but that's a known limitation.
		return map[string]any{"ok": true, "removed": 0}, nil
	}
	dir := filepath.Dir(h.imagesIndexPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.delete: mkdir: " + err.Error()}
	}
	body, err := json.MarshalIndent(imagesIndexFile{Items: kept}, "", "  ")
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.delete: marshal: " + err.Error()}
	}
	tmp := h.imagesIndexPath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.delete: write: " + err.Error()}
	}
	if err := os.Rename(tmp, h.imagesIndexPath()); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.delete: rename: " + err.Error()}
	}
	return map[string]any{"ok": true, "removed": removed}, nil
}

// --- config + workflow loading --------------------------------------------

type comfyUIConfig struct {
	BaseURL string
	// Workflow source: exactly one of WorkflowJSON / WorkflowPath
	// drives readAndPatchWorkflow. WorkflowJSON is pre-loaded bytes
	// (used by builtin workflows shipped via embed); WorkflowPath
	// is read from disk lazily (used by the user's exported
	// workflow). Both empty is invalid.
	WorkflowJSON         []byte
	WorkflowPath         string
	PromptNodeID         string
	NegativePromptNodeID string
	SeedNodeID           string
}

const (
	defaultComfyUIBaseURL  = "http://localhost:8188"
	defaultWorkflowRelPath = "images/comfyui-default.json"
)

// loadComfyUIConfig reads images.providers.comfyui.* from the merged
// config, applying defaults for the base URL and workflow path. The
// loopback rewrite mirrors the LM Studio integration so a config of
// "localhost:8188" Just Works whether talon is local or in Docker.
//
// workflowID picks one of the shipped builtin workflows when set; an
// empty string falls back to the user's config-driven workflow. For
// builtins, the prompt/negative/seed node ids are taken from the
// builtin entry rather than config — they're pinned to match the
// shipped JSON so users don't have to discover/configure them.
func (h *ImagesHandler) loadComfyUIConfig(workflowID string) (comfyUIConfig, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return comfyUIConfig{}, &FrameError{Code: ErrCodeInternal, Message: "images: read config: " + err.Error()}
	}
	cfg := comfyUIConfig{
		BaseURL: defaultComfyUIBaseURL,
	}
	if v := gjson.GetBytes(merged, "images.providers.comfyui.baseUrl"); v.Exists() && v.Str != "" {
		cfg.BaseURL = v.Str
	}
	cfg.BaseURL = netutil.RewriteLoopbackForContainer(cfg.BaseURL)

	// Builtin path: look up the entry, load its embedded JSON into
	// cfg.WorkflowJSON, copy the pinned node ids. WorkflowPath stays
	// empty so readAndPatchWorkflow uses the in-memory bytes.
	if entry := findBuiltinWorkflow(workflowID); entry != nil {
		raw, err := loadBuiltinWorkflowJSON(entry)
		if err != nil {
			return comfyUIConfig{}, &FrameError{Code: ErrCodeInternal, Message: "images: " + err.Error()}
		}
		cfg.WorkflowJSON = raw
		cfg.PromptNodeID = entry.PromptNodeID
		cfg.NegativePromptNodeID = entry.NegativePromptNodeID
		cfg.SeedNodeID = entry.SeedNodeID
		return cfg, nil
	}
	// Reject an unknown workflowId rather than silently falling back —
	// the UI should only ever send ids it got from
	// images.workflows.list, so a typo there is a real bug.
	if strings.TrimSpace(workflowID) != "" {
		return comfyUIConfig{}, &FrameError{
			Code:    ErrCodeBadRequest,
			Message: fmt.Sprintf("images: unknown workflowId %q (call images.workflows.list for valid ids)", workflowID),
		}
	}

	// User's config-driven workflow.
	cfg.WorkflowPath = filepath.Join(h.paths.Talon.Dir, defaultWorkflowRelPath)
	if v := gjson.GetBytes(merged, "images.providers.comfyui.workflow.path"); v.Exists() && v.Str != "" {
		cfg.WorkflowPath = expandHomePath(v.Str)
	}
	// Node ids are strings in ComfyUI but users frequently type them
	// unquoted ("6") into config. Result.String() auto-coerces both
	// JSON strings and numbers, so either input works.
	if v := gjson.GetBytes(merged, "images.providers.comfyui.workflow.promptNodeId"); v.Exists() {
		cfg.PromptNodeID = v.String()
	}
	if v := gjson.GetBytes(merged, "images.providers.comfyui.workflow.negativePromptNodeId"); v.Exists() {
		cfg.NegativePromptNodeID = v.String()
	}
	if v := gjson.GetBytes(merged, "images.providers.comfyui.workflow.seedNodeId"); v.Exists() {
		cfg.SeedNodeID = v.String()
	}
	// PromptNodeID validation moved to readAndPatchWorkflow — list/
	// fetch handlers only need BaseURL, so requiring a configured
	// node id at config-load time would lock them out for users
	// who haven't exported a workflow yet.
	return cfg, nil
}

// fileExists reports whether p resolves to an existing file. Used by
// images.workflows.list to skip the user-default row when the file
// isn't on disk yet (avoids a misleading "Default" entry that would
// fail on submit).
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// readAndPatchWorkflow loads the workflow JSON (from cfg.WorkflowJSON
// when populated, otherwise cfg.WorkflowPath) and overrides the
// user-controlled fields: positive prompt text, optional negative
// prompt, optional seed (random when not supplied so successive runs
// don't collapse to identical outputs). The workflow is otherwise
// passed through verbatim — the user's checkpoint, sampler, dims, etc.
// stay in their hands.
func readAndPatchWorkflow(cfg comfyUIConfig, p imagesGenerateParams) (json.RawMessage, *FrameError) {
	if cfg.PromptNodeID == "" {
		return nil, &FrameError{
			Code:    ErrCodeBadRequest,
			Message: "images: images.providers.comfyui.workflow.promptNodeId is not configured (set the node id of the positive-prompt CLIPTextEncode in your exported workflow, or pick a builtin via workflowId)",
		}
	}
	var raw []byte
	if len(cfg.WorkflowJSON) > 0 {
		raw = cfg.WorkflowJSON
	} else {
		var err error
		raw, err = os.ReadFile(cfg.WorkflowPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, &FrameError{
					Code:    ErrCodeBadRequest,
					Message: "images: workflow file not found at " + cfg.WorkflowPath + " — export an API-format workflow from ComfyUI and save it there (or set images.providers.comfyui.workflow.path)",
				}
			}
			return nil, &FrameError{Code: ErrCodeInternal, Message: "images: read workflow: " + err.Error()}
		}
	}
	var workflow map[string]any
	if err := json.Unmarshal(raw, &workflow); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images: workflow JSON invalid: " + err.Error()}
	}

	if err := setWorkflowText(workflow, cfg.PromptNodeID, p.Prompt); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images: " + err.Error()}
	}
	if cfg.NegativePromptNodeID != "" && p.NegativePrompt != "" {
		if err := setWorkflowText(workflow, cfg.NegativePromptNodeID, p.NegativePrompt); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images: " + err.Error()}
		}
	}
	if cfg.SeedNodeID != "" {
		seed := int64(0)
		if p.Seed != nil {
			seed = *p.Seed
		} else {
			s, err := randomSeed()
			if err != nil {
				return nil, &FrameError{Code: ErrCodeInternal, Message: "images: seed: " + err.Error()}
			}
			seed = s
		}
		if err := setWorkflowSeed(workflow, cfg.SeedNodeID, seed); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images: " + err.Error()}
		}
	}

	out, err := json.Marshal(workflow)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images: marshal patched workflow: " + err.Error()}
	}
	return out, nil
}

// setWorkflowText writes inputs.text on the named node. Errors fail
// loud rather than silently no-op'ing — a misconfigured node id is the
// kind of bug that's nearly impossible to debug from a "no image
// appeared" symptom.
func setWorkflowText(workflow map[string]any, nodeID, text string) error {
	inputs, err := nodeInputs(workflow, nodeID)
	if err != nil {
		return err
	}
	inputs["text"] = text
	return nil
}

// setWorkflowSeed writes inputs.seed (most KSamplers) or
// inputs.noise_seed (some sampler variants). We try seed first; if
// the existing inputs map has noise_seed instead, we use that.
func setWorkflowSeed(workflow map[string]any, nodeID string, seed int64) error {
	inputs, err := nodeInputs(workflow, nodeID)
	if err != nil {
		return err
	}
	if _, ok := inputs["noise_seed"]; ok {
		inputs["noise_seed"] = seed
		return nil
	}
	inputs["seed"] = seed
	return nil
}

func nodeInputs(workflow map[string]any, nodeID string) (map[string]any, error) {
	node, ok := workflow[nodeID].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow has no node %q", nodeID)
	}
	inputs, ok := node["inputs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow node %q has no inputs object", nodeID)
	}
	return inputs, nil
}

// formatExecutionError extracts the meaningful failure detail from a
// ComfyUI execution_error event payload. ComfyUI sends node_id +
// node_type + exception_type + exception_message; any subset may be
// present in older / partial events. We compose them into a single
// line that's safe to render verbatim in the UI's error banner.
//
//	"CheckpointLoaderSimple (node 1): ValueError: ckpt_name '...' not found"
//	"comfyui execution_error" — fallback when nothing useful is in the payload
func formatExecutionError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "comfyui execution_error"
	}
	var d struct {
		NodeID           any    `json:"node_id"`
		NodeType         string `json:"node_type"`
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "comfyui execution_error"
	}
	parts := []string{}
	switch v := d.NodeID.(type) {
	case string:
		if v != "" {
			if d.NodeType != "" {
				parts = append(parts, fmt.Sprintf("%s (node %s)", d.NodeType, v))
			} else {
				parts = append(parts, fmt.Sprintf("node %s", v))
			}
		} else if d.NodeType != "" {
			parts = append(parts, d.NodeType)
		}
	case float64:
		if d.NodeType != "" {
			parts = append(parts, fmt.Sprintf("%s (node %d)", d.NodeType, int64(v)))
		} else {
			parts = append(parts, fmt.Sprintf("node %d", int64(v)))
		}
	default:
		if d.NodeType != "" {
			parts = append(parts, d.NodeType)
		}
	}
	if d.ExceptionType != "" && d.ExceptionMessage != "" {
		parts = append(parts, d.ExceptionType+": "+d.ExceptionMessage)
	} else if d.ExceptionMessage != "" {
		parts = append(parts, d.ExceptionMessage)
	} else if d.ExceptionType != "" {
		parts = append(parts, d.ExceptionType)
	}
	if len(parts) == 0 {
		return "comfyui execution_error"
	}
	return "comfyui execution_error: " + strings.Join(parts, ": ")
}

// expandHomePath swaps a leading "~/" for the user's home dir; other
// paths are returned verbatim. Keeps config files portable across user
// accounts without baking absolute paths into the JSON.
func expandHomePath(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

func newImagesRunID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomSeed returns a positive 63-bit seed so it survives JSON
// round-tripping (browsers parse >2^53 ints as floats — capping at
// 2^53 keeps round-trip exact). Most ComfyUI samplers happily accept
// this range.
func randomSeed() (int64, error) {
	b := make([]byte, 7) // 56 bits, well within JS safe-integer range
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	v := int64(0)
	for _, x := range b {
		v = (v << 8) | int64(x)
	}
	return v, nil
}
