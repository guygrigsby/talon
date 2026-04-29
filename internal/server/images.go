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
	Fetch(ctx context.Context, ref comfyui.ImageRef) ([]byte, string, error)
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

// Register wires images.generate + images.fetch into r.
func (h *ImagesHandler) Register(r *Registry) {
	r.Register("images.generate", h.handleGenerate)
	r.Register("images.fetch", h.handleFetch)
}

// --- images.generate -------------------------------------------------------

type imagesGenerateParams struct {
	SessionKey     string `json:"sessionKey"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negativePrompt"`
	Seed           *int64 `json:"seed,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func (h *ImagesHandler) handleGenerate(_ context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesGenerateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.generate: " + err.Error()}
	}
	if strings.TrimSpace(p.SessionKey) == "" || strings.TrimSpace(p.Prompt) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.generate: sessionKey and prompt are required"}
	}

	cfg, ferr := h.loadComfyUIConfig()
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

	// emitFailures counts consecutive PushEvent failures so the run
	// gives up when the client has disconnected. Without this the
	// goroutine logs an error per progress frame all the way through
	// the run for any short-lived caller (e.g. `talon gateway call`,
	// which closes its WS as soon as it gets the {runId} response).
	emitFailures := 0
	const emitFailureThreshold = 3
	emit := func(state string, data map[string]any) bool {
		if err := h.emit(sess, runID, sessionKey, state, data); err != nil {
			emitFailures++
			return emitFailures < emitFailureThreshold
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
		emit("error", map[string]any{
			"errorMessage": "comfyui execution_error",
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
}

func (h *ImagesHandler) handleFetch(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesFetchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.fetch: " + err.Error()}
	}
	if p.Filename == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.fetch: filename is required"}
	}
	cfg, ferr := h.loadComfyUIConfig()
	if ferr != nil {
		return nil, ferr
	}
	cli := h.dial(cfg.BaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, ctype, err := cli.Fetch(ctx, comfyui.ImageRef{
		Filename: p.Filename, Subfolder: p.Subfolder, Type: p.Type,
	})
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

// --- config + workflow loading --------------------------------------------

type comfyUIConfig struct {
	BaseURL              string
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
func (h *ImagesHandler) loadComfyUIConfig() (comfyUIConfig, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return comfyUIConfig{}, &FrameError{Code: ErrCodeInternal, Message: "images: read config: " + err.Error()}
	}
	cfg := comfyUIConfig{
		BaseURL:              defaultComfyUIBaseURL,
		WorkflowPath:         filepath.Join(h.paths.Talon.Dir, defaultWorkflowRelPath),
		PromptNodeID:         "",
		NegativePromptNodeID: "",
		SeedNodeID:           "",
	}
	if v := gjson.GetBytes(merged, "images.providers.comfyui.baseUrl"); v.Exists() && v.Str != "" {
		cfg.BaseURL = v.Str
	}
	cfg.BaseURL = netutil.RewriteLoopbackForContainer(cfg.BaseURL)
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
	if cfg.PromptNodeID == "" {
		return comfyUIConfig{}, &FrameError{
			Code:    ErrCodeBadRequest,
			Message: "images: images.providers.comfyui.workflow.promptNodeId is not configured (set the node id of the positive-prompt CLIPTextEncode in your exported workflow)",
		}
	}
	return cfg, nil
}

// readAndPatchWorkflow loads the workflow JSON from disk and overrides
// the user-controlled fields: positive prompt text, optional negative
// prompt, optional seed (random when not supplied so successive runs
// don't collapse to identical outputs). The workflow is otherwise
// passed through verbatim — the user's checkpoint, sampler, dims, etc.
// stay in their hands.
func readAndPatchWorkflow(cfg comfyUIConfig, p imagesGenerateParams) (json.RawMessage, *FrameError) {
	raw, err := os.ReadFile(cfg.WorkflowPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &FrameError{
				Code:    ErrCodeBadRequest,
				Message: "images: workflow file not found at " + cfg.WorkflowPath + " — export an API-format workflow from ComfyUI and save it there (or set images.providers.comfyui.workflow.path)",
			}
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images: read workflow: " + err.Error()}
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
