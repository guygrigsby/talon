package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/agentcontext"
	"github.com/guygrigsby/talon/internal/provider"
)

// AgentResolver looks up the model an agent should drive a chat with. The
// gateway's chat.send handler calls this once per send to translate the
// agentId (parsed from sessionKey) into a concrete ModelID. Returns
// ErrAgentNotFound when the agent does not exist.
type AgentResolver interface {
	PrimaryModel(agentID string) (provider.ModelID, error)
}

// WorkspaceResolver yields the filesystem path the agent operates against.
// Returned by ChatHandler when constructing the per-call tool registry.
// Implementations should mirror AgentResolver's three-tier precedence
// (per-agent workspace, then agents.defaults.workspace).
type WorkspaceResolver interface {
	Workspace(agentID string) (string, error)
}

// ProviderFactory yields the provider that serves a given provider name on
// behalf of a given agent. The agent context lets the factory locate
// per-agent credentials (e.g. <openclaw>/agents/<agentId>/agent/auth-profiles.json).
// Returns ErrProviderUnavailable when the provider is not implemented.
type ProviderFactory interface {
	For(providerName, agentID string) (provider.Provider, error)
}

// ErrAgentNotFound is returned by AgentResolver when no matching agent
// is configured.
var ErrAgentNotFound = errors.New("agent not found")

// ErrProviderUnavailable is returned by ProviderFactory when the named
// provider has no implementation available in this build.
var ErrProviderUnavailable = errors.New("provider unavailable")

// ChatStore is the in-memory message history shared across all sessions
// addressed by a sessionKey. The chat.send handler appends user turns,
// assistant turns (with optional tool_calls), and tool result turns here.
// Messages do not persist across gateway restarts in this MVP (talon-2dl
// will persist if needed).
type ChatStore struct {
	mu      sync.Mutex
	history map[string][]ChatMessage
}

// ChatMessage is a single turn stored in the history. Shape depends on Role:
//
//   - "user"      → Content
//   - "assistant" → Content (may be empty if turn was tool-only) + ToolCalls
//   - "tool"      → Content (tool output) + ToolCallID + ToolName
//                   (ToolName is denormalized from the originating ToolCall
//                   so chat.history rows can label the result without
//                   re-walking the assistant turn that issued the call.)
type ChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []provider.ToolCall
	ToolCallID string
	ToolName   string
	At         time.Time
}

// NewChatStore returns an empty in-memory store.
func NewChatStore() *ChatStore {
	return &ChatStore{history: make(map[string][]ChatMessage)}
}

// Append adds a plain text message to sessionKey's history.
func (s *ChatStore) Append(sessionKey, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[sessionKey] = append(s.history[sessionKey], ChatMessage{Role: role, Content: content, At: time.Now()})
}

// AppendAssistantWithCalls records an assistant turn that may carry tool_calls
// alongside (optional) text content. Used by the multi-turn loop after a
// stream concludes with one or more DeltaToolCall events.
func (s *ChatStore) AppendAssistantWithCalls(sessionKey, content string, calls []provider.ToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[sessionKey] = append(s.history[sessionKey], ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: calls,
		At:        time.Now(),
	})
}

// AppendToolResult records a tool execution result tied to a prior
// assistant tool_use. Stored as role=tool with the originating ToolCallID
// and ToolName (denormalized from the assistant turn so chat.history
// readers don't have to walk back to find the name).
func (s *ChatStore) AppendToolResult(sessionKey, toolCallID, toolName, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[sessionKey] = append(s.history[sessionKey], ChatMessage{
		Role:       "tool",
		Content:    output,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		At:         time.Now(),
	})
}

// Snapshot returns a copy of the stored messages for sessionKey, oldest
// first. Returns nil for unknown sessionKeys.
func (s *ChatStore) Snapshot(sessionKey string) []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.history[sessionKey]
	if len(src) == 0 {
		return nil
	}
	out := make([]ChatMessage, len(src))
	copy(out, src)
	return out
}

// Keys returns the sessionKeys with at least one stored message. Used by
// sessions.list to surface chat-only sessions even if they've never been
// patched.
func (s *ChatStore) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.history))
	for k := range s.history {
		out = append(out, k)
	}
	return out
}

// ToolRunner executes a tool by name and returns the rendered result.
// Implementations are expected to be safe for concurrent use across
// distinct chat runs.
type ToolRunner interface {
	Run(ctx context.Context, name string, input json.RawMessage) (string, error)
	Specs() []provider.ToolSpec
}

// ChatHandler is the chat.send registry handler. Construct one with
// NewChatHandler, then register it on the server's Registry.
type ChatHandler struct {
	resolver  AgentResolver
	factory   ProviderFactory
	workspace WorkspaceResolver
	tools     func(workspace string) ToolRunner
	store     *ChatStore
	sessions  *SessionStore

	// runs tracks active runs by "sessionKey|idempotencyKey" so a duplicate
	// chat.send returns the same runId without spawning a second stream.
	runsMu sync.Mutex
	runs   map[string]string

	// StreamTimeout caps a single chat.send invocation across all
	// multi-turn iterations. Default 5 minutes; override in tests.
	StreamTimeout time.Duration
	// MaxToolIterations bounds the multi-turn loop so a runaway model
	// can't spin tool calls forever. Default 16; override in tests.
	MaxToolIterations int
}

// NewChatHandler constructs a ChatHandler that uses resolver to look up
// agent models, factory to materialize providers, and store to record
// history. All three are required; nil values will cause the handler to
// reject sends with INTERNAL errors. WorkspaceResolver and tools are
// optional — when both are non-nil chat.send advertises tools to the
// model and runs them in a multi-turn loop. When either is nil, chat.send
// degrades to text-only single-turn (the previous behavior).
func NewChatHandler(resolver AgentResolver, factory ProviderFactory, store *ChatStore) *ChatHandler {
	return &ChatHandler{
		resolver:          resolver,
		factory:           factory,
		store:             store,
		runs:              make(map[string]string),
		StreamTimeout:     5 * time.Minute,
		MaxToolIterations: 16,
	}
}

// WithTools enables tool calling by wiring a workspace resolver and a
// per-workspace ToolRunner factory. Pass nil to either to keep the handler
// in text-only mode.
func (h *ChatHandler) WithTools(ws WorkspaceResolver, mk func(workspace string) ToolRunner) *ChatHandler {
	h.workspace = ws
	h.tools = mk
	return h
}

// WithSessions enables per-session UI overrides. When set, runStream
// consults sessions.Model(sessionKey) before falling back to the agent's
// PrimaryModel — that's how the UI's chat-model picker actually changes
// the model that gets queried.
func (h *ChatHandler) WithSessions(sessions *SessionStore) *ChatHandler {
	h.sessions = sessions
	return h
}

// Register wires the handler's methods into r. Registers chat.send and
// chat.history.
func (h *ChatHandler) Register(r *Registry) {
	r.Register("chat.send", h.handleSend)
	r.Register("chat.history", h.handleHistory)
}

// chatHistoryParams matches openclaw's chat.history request shape (subset).
type chatHistoryParams struct {
	SessionKey string `json:"sessionKey"`
	Limit      int    `json:"limit"`
}

// openclawMeta is the per-row envelope decoration the openclaw web UI uses
// for stable React keys.
type openclawMeta struct {
	ID  string `json:"id"`
	Seq int    `json:"seq"`
}

// historyContentPart is a content block in chat.history rows. Two flavors:
//
//   - {type: "text", text: "..."}                         visible reply text
//   - {type: "tool_use", id, name, input: <decoded JSON>} assistant invoking a tool
//
// Tool result rows use a flat content shape ({type:"text", text:<output>})
// because openclaw's UI labels them via row-level toolName/toolCallId
// fields rather than nested blocks.
type historyContentPart struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

func (h *ChatHandler) handleHistory(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	if h.store == nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.history: no store wired"}
	}
	var p chatHistoryParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "chat.history: " + err.Error()}
		}
	}
	if strings.TrimSpace(p.SessionKey) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "chat.history: sessionKey is required"}
	}

	msgs := h.store.Snapshot(p.SessionKey)
	// limit<=0 means "no limit" per openclaw convention.
	if p.Limit > 0 && len(msgs) > p.Limit {
		msgs = msgs[len(msgs)-p.Limit:]
	}

	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		out[i] = renderHistoryRow(p.SessionKey, i, m)
	}
	return map[string]any{"messages": out}, nil
}

// renderHistoryRow translates one ChatMessage into the openclaw-shaped
// row the web UI consumes. Three role variants matter:
//
//   - "user":      flat {role:"user", content:[{type:"text",text}]}
//   - "assistant": content array carries any visible text plus tool_use
//                  blocks (one per ToolCall). Empty text is omitted so a
//                  pure tool-call turn doesn't render a blank bubble.
//   - "tool":      role re-labeled to "toolResult" — that's what
//                  openclaw's chat-message renderer matches on. Includes
//                  toolCallId + toolName at the row level so the UI can
//                  label the card with the actual tool name (e.g. "glob")
//                  instead of falling back to a generic "tool" sublabel.
func renderHistoryRow(sessionKey string, i int, m ChatMessage) map[string]any {
	row := map[string]any{
		"__openclaw": openclawMeta{ID: messageID(sessionKey, i), Seq: i + 1},
		"timestamp":  m.At.UnixMilli(),
	}
	switch m.Role {
	case "tool":
		row["role"] = "toolResult"
		row["toolCallId"] = m.ToolCallID
		if m.ToolName != "" {
			row["toolName"] = m.ToolName
		}
		row["isError"] = false
		row["content"] = []historyContentPart{{Type: "text", Text: m.Content}}
	case "assistant":
		row["role"] = "assistant"
		blocks := make([]historyContentPart, 0, 1+len(m.ToolCalls))
		if m.Content != "" {
			blocks = append(blocks, historyContentPart{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			var input any
			if tc.ArgumentsJSON != "" {
				if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &input); err != nil {
					input = tc.ArgumentsJSON
				}
			}
			blocks = append(blocks, historyContentPart{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		row["content"] = blocks
	default: // user, system, anything else
		row["role"] = m.Role
		row["content"] = []historyContentPart{{Type: "text", Text: m.Content}}
	}
	return row
}

// messageID returns a deterministic, sessionKey-scoped id for the i'th
// message. Stable across reads (so React keys don't churn) and unique
// within a session.
func messageID(sessionKey string, i int) string {
	h := fnv64(sessionKey)
	return fmtHex8(h ^ uint64(i+1))
}

// fnv64 is a tiny FNV-1a hash to avoid pulling in hash/fnv just for this.
func fnv64(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

func fmtHex8(v uint64) string {
	const hex = "0123456789abcdef"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = hex[v&0xf]
		v >>= 4
	}
	return string(b[:])
}

// chatSendParams matches openclaw's chat.send request shape (subset).
type chatSendParams struct {
	SessionKey     string `json:"sessionKey"`
	Message        string `json:"message"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func (h *ChatHandler) handleSend(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	if h.resolver == nil || h.factory == nil || h.store == nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat is not wired (no resolver/factory/store)"}
	}
	var p chatSendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "chat.send: " + err.Error()}
	}
	if strings.TrimSpace(p.SessionKey) == "" || strings.TrimSpace(p.Message) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "chat.send: sessionKey and message are required"}
	}

	agentID := AgentIDFromSessionKey(p.SessionKey)
	if agentID == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "chat.send: cannot derive agent from sessionKey " + p.SessionKey}
	}

	model, err := h.resolver.PrimaryModel(agentID)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: resolve agent: " + err.Error()}
	}
	// Per-session UI override (sessions.patch model:"...") wins over the
	// agent default. Without this, the chat-model picker is cosmetic.
	if h.sessions != nil {
		if override := h.sessions.Model(p.SessionKey); override != "" {
			model = provider.ModelID(override)
		}
	}
	providerName := model.Provider()
	if providerName == "" {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: model is missing a provider segment: " + string(model)}
	}

	prov, err := h.factory.For(providerName, agentID)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: provider: " + err.Error()}
	}

	// runId protocol contract: the openclaw web UI generates a UUID
	// client-side, sets chatRunId = uuid, and passes it as
	// idempotencyKey on chat.send (it does NOT send a separate runId
	// param). chat events MUST echo back that same value as runId so the
	// UI's handleChatEvent matches them against state.chatRunId. Mismatch
	// triggers the "different run" branch, which appends the final
	// message but never clears chatRunId — and that leaves the typing
	// indicator on forever.
	//
	// So: idempotencyKey IS the runId. When empty we mint a fresh one for
	// CLI callers that don't supply their own.
	runID := p.IdempotencyKey
	if runID == "" {
		fresh, err := newRunID()
		if err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: " + err.Error()}
		}
		runID = fresh
	}
	runKey := p.SessionKey + "|" + runID
	h.runsMu.Lock()
	if _, ok := h.runs[runKey]; ok {
		h.runsMu.Unlock()
		return map[string]any{"runId": runID}, nil
	}
	h.runs[runKey] = runID
	h.runsMu.Unlock()

	h.store.Append(p.SessionKey, "user", p.Message)

	go h.runStream(hc.Session, runID, p.SessionKey, agentID, prov, model, runKey)

	return map[string]any{"runId": runID}, nil
}

// emitTarget collects the destination + identifiers for events fired
// during a chat run. Decoupled into chat (delta/final/error) and tool
// (agent.tool start/result) sinks because subagent runs want tool
// visibility in the parent's UI but must NOT emit chat-text events into
// the parent's transcript.
//
//	top-level chat.send: both chatSess and toolSess are the user's session.
//	subagent RunInline:   chatSess is nil (no UI text pollution); toolSess
//	                      is the parent's session so subagent tool calls
//	                      surface in the parent's tool stream.
//
// runID/sessionKey identify the OUTER run as far as the UI is concerned —
// for subagents, those are the parent's values so the UI filters match.
type emitTarget struct {
	chatSess   *Session
	toolSess   *Session
	runID      string
	sessionKey string
}

// emitContextKey carries the active emitTarget through tool invocations.
// The subagent tool reads it to forward its own tool events into the
// parent's stream.
type emitContextKey struct{}

func withEmitTarget(ctx context.Context, e emitTarget) context.Context {
	return context.WithValue(ctx, emitContextKey{}, e)
}

// EmitTargetFromContext exposes the active emit target to tools that
// want to nest their own activity under the outer run (e.g. subagent
// invocations forwarding tool stream events to the parent's UI).
// Returns the zero value if no run is active in this context.
func EmitTargetFromContext(ctx context.Context) (parentSess *Session, parentRunID, parentSessionKey string) {
	v, _ := ctx.Value(emitContextKey{}).(emitTarget)
	return v.toolSess, v.runID, v.sessionKey
}

// runStream is the goroutine launched per chat.send. It owns the runs-map
// cleanup and the StreamTimeout context, then delegates the per-iteration
// work to runChatLoop. Streaming events fan out via sess; the final text
// is discarded here (callers like RunInline use runChatLoop directly).
func (h *ChatHandler) runStream(sess *Session, runID, sessionKey, agentID string, prov provider.Provider, model provider.ModelID, runKey string) {
	defer func() {
		if runKey != "" {
			h.runsMu.Lock()
			delete(h.runs, runKey)
			h.runsMu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), h.StreamTimeout)
	defer cancel()
	target := emitTarget{chatSess: sess, toolSess: sess, runID: runID, sessionKey: sessionKey}
	_, _ = h.runChatLoop(ctx, target, sessionKey, agentID, prov, model)
}

// runChatLoop drives the multi-turn chat loop and returns the accumulated
// assistant text. Each iteration: build messages from store → stream the
// provider → handle text + tool calls → if tools were invoked, run them
// (in parallel when there's more than one) and append results, then
// re-stream → otherwise emit final and return. Capped at
// MaxToolIterations to prevent runaway.
//
// emit fields control where events fire:
//   - chatSess receives chat (delta/final/error) events; nil suppresses
//   - toolSess receives agent.tool start/result events; nil suppresses
//
// storeKey is the ChatStore key for this run's history (different from
// emit.sessionKey for subagent runs, which want their own history but
// surface tool activity under the parent's sessionKey).
func (h *ChatHandler) runChatLoop(ctx context.Context, emit emitTarget, storeKey, agentID string, prov provider.Provider, model provider.ModelID) (string, error) {
	// Resolve the agent's workspace once per loop. Used for two purposes:
	// tool execution (when a runner is configured) and system-prompt
	// composition from the workspace's context markdown files.
	var workspace string
	if h.workspace != nil {
		if ws, err := h.workspace.Workspace(agentID); err == nil {
			workspace = ws
		}
	}
	var runner ToolRunner
	if workspace != "" && h.tools != nil {
		runner = h.tools(workspace)
	}
	systemPrompt := agentcontext.Build(workspace)

	var seq int
	var accumulated strings.Builder // visible assistant text across iterations

	// Stash the emit target on ctx so tools (e.g. subagent) can forward
	// nested events into the same parent stream.
	ctx = withEmitTarget(ctx, emit)

	for iter := 0; iter < h.MaxToolIterations; iter++ {
		history := h.store.Snapshot(storeKey)
		reqMsgs := messagesFromHistory(history)
		req := provider.Request{Model: model, Messages: reqMsgs, System: systemPrompt}
		if runner != nil {
			req.Tools = runner.Specs()
		}

		deltaCh, err := prov.Stream(ctx, req)
		if err != nil {
			seq++
			_ = h.emitError(emit.chatSess, emit.runID, emit.sessionKey, seq, "provider", err.Error())
			return accumulated.String(), err
		}

		var iterText strings.Builder
		var toolCalls []provider.ToolCall
		emitFailures := 0

		for d := range deltaCh {
			switch d.Kind {
			case provider.DeltaText:
				iterText.WriteString(d.Text)
				accumulated.WriteString(d.Text)
				seq++
				if err := h.emitChat(emit.chatSess, emit.runID, emit.sessionKey, seq, "delta", accumulated.String()); err != nil {
					emitFailures++
					// Subagent mode (chatSess=nil): every emit fails;
					// don't abort — the caller wants the text.
					if emit.chatSess != nil && emitFailures >= 3 {
						for range deltaCh {
						}
						return accumulated.String(), nil
					}
				}
			case provider.DeltaToolCall:
				if d.ToolCall != nil {
					toolCalls = append(toolCalls, *d.ToolCall)
				}
			case provider.DeltaError:
				seq++
				_ = h.emitError(emit.chatSess, emit.runID, emit.sessionKey, seq, "provider", d.Err.Error())
				return accumulated.String(), d.Err
			case provider.DeltaUsage:
				// usage not surfaced over the wire yet.
			}
		}

		if len(toolCalls) == 0 {
			// Terminal turn — assistant produced text and no tool calls.
			text := iterText.String()
			if text != "" {
				h.store.Append(storeKey, "assistant", text)
			}
			seq++
			_ = h.emitChat(emit.chatSess, emit.runID, emit.sessionKey, seq, "final", accumulated.String())
			return accumulated.String(), nil
		}

		// Persist the assistant turn that emitted the tool calls so the
		// next iteration's history reflects them.
		h.store.AppendAssistantWithCalls(storeKey, iterText.String(), toolCalls)

		if runner == nil {
			// Model wanted tools but we have nowhere to run them. Surface
			// a clear error rather than looping forever.
			seq++
			err := errors.New("model invoked tools but no tool runner is configured for this agent")
			_ = h.emitError(emit.chatSess, emit.runID, emit.sessionKey, seq, "tool-runner-unavailable", err.Error())
			return accumulated.String(), err
		}

		// Run all tool calls in parallel (up to maxToolConcurrency at a
		// time). Tool events fire into emit.toolSess (parent's session
		// for subagent runs). Outputs collect in the model's call order
		// so the next turn's history is deterministic.
		outputs := h.runToolsParallel(ctx, emit, runner, toolCalls)
		for i, tc := range toolCalls {
			h.store.AppendToolResult(storeKey, tc.ID, tc.Name, outputs[i])
		}
	}

	// Iteration cap reached — model kept invoking tools without
	// converging. Surface as a structured error.
	seq++
	err := fmt.Errorf("exceeded %d tool iterations without producing a final reply", h.MaxToolIterations)
	_ = h.emitError(emit.chatSess, emit.runID, emit.sessionKey, seq, "tool-loop-limit", err.Error())
	return accumulated.String(), err
}

// maxToolConcurrency caps how many tool calls run concurrently in a
// single turn. The model picks 1-N tools per turn; running them in
// parallel cuts wall-clock for independent calls (especially useful for
// subagents). 4 is a safe default — high enough to parallelize typical
// turns, low enough to avoid swamping shared resources or hitting
// per-provider rate limits.
const maxToolConcurrency = 4

// runToolsParallel executes toolCalls concurrently and returns outputs in
// the original call order. Tool errors are captured into the output
// string so the model can react; this function never returns its own
// error. Stream events for each call (start/result) fire from the
// goroutine that runs it via emit.toolSess.
func (h *ChatHandler) runToolsParallel(ctx context.Context, emit emitTarget, runner ToolRunner, toolCalls []provider.ToolCall) []string {
	outputs := make([]string, len(toolCalls))
	if len(toolCalls) == 0 {
		return outputs
	}
	runOne := func(tc provider.ToolCall) string {
		h.emitAgentToolStart(emit.toolSess, emit.runID, emit.sessionKey, tc.ID, tc.Name, tc.ArgumentsJSON)
		out, err := runner.Run(ctx, tc.Name, json.RawMessage(tc.ArgumentsJSON))
		if err != nil {
			out = "ERROR: " + err.Error()
		}
		h.emitAgentToolResult(emit.toolSess, emit.runID, emit.sessionKey, tc.ID, tc.Name, out)
		return out
	}
	if len(toolCalls) == 1 {
		// Fast path — no goroutine overhead for the common single-call
		// case.
		outputs[0] = runOne(toolCalls[0])
		return outputs
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxToolConcurrency)
	for i, tc := range toolCalls {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tc provider.ToolCall) {
			defer wg.Done()
			defer func() { <-sem }()
			outputs[i] = runOne(tc)
		}(i, tc)
	}
	wg.Wait()
	return outputs
}

// RunInline executes a chat.send-like loop without producing chat events
// for the caller. Used by the subagent tool: the parent's chat.send is
// paused on the tool call, this function runs the named agent's full
// multi-turn loop, and returns the accumulated assistant text for the
// parent to feed back as a tool result.
//
// History is stored under a fresh "subagent:<agentId>:<runId>" key so
// the subagent's transcript doesn't pollute the parent's. Tool events
// (agent.tool start/result) forward into the parent's session via the
// emit target stashed on parentCtx by the parent's runChatLoop — that's
// how the subagent's tool calls show up live in the parent's UI tool
// stream. Subagent runs don't honor SessionStore model overrides; they
// always use the target agent's PrimaryModel.
func (h *ChatHandler) RunInline(parentCtx context.Context, agentID, message string) (string, error) {
	if h.resolver == nil || h.factory == nil || h.store == nil {
		return "", errors.New("chat is not wired (no resolver/factory/store)")
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(message) == "" {
		return "", errors.New("subagent: agentId and message are required")
	}
	model, err := h.resolver.PrimaryModel(agentID)
	if err != nil {
		return "", fmt.Errorf("subagent resolve: %w", err)
	}
	providerName := model.Provider()
	if providerName == "" {
		return "", fmt.Errorf("subagent: model %q has no provider segment", model)
	}
	prov, err := h.factory.For(providerName, agentID)
	if err != nil {
		return "", fmt.Errorf("subagent provider: %w", err)
	}
	subRunID, err := newRunID()
	if err != nil {
		return "", err
	}
	storeKey := "subagent:" + agentID + ":" + subRunID

	// Parent's emit target — present iff this RunInline was invoked from
	// inside another runChatLoop (e.g. via the subagent tool). Forward
	// our tool events into the parent's tool stream so the user sees
	// what the subagent is doing live; suppress chat events so the
	// subagent's text doesn't write into the parent's transcript.
	parentEmit, _ := parentCtx.Value(emitContextKey{}).(emitTarget)
	emit := emitTarget{
		chatSess:   nil, // never emit chat to parent — the subagent's text lands as a tool result instead
		toolSess:   parentEmit.toolSess,
		runID:      parentEmit.runID,
		sessionKey: parentEmit.sessionKey,
	}
	if emit.runID == "" {
		// No parent context (e.g. CLI invocation). Use our own values
		// so events still tag correctly even if no one's listening.
		emit.runID = subRunID
		emit.sessionKey = storeKey
	}

	h.store.Append(storeKey, "user", message)

	ctx, cancel := context.WithTimeout(parentCtx, h.StreamTimeout)
	defer cancel()
	return h.runChatLoop(ctx, emit, storeKey, agentID, prov, model)
}

// messagesFromHistory translates ChatStore rows into provider.Messages,
// preserving tool_calls (for assistant turns invoking tools) and
// ToolCallID (for tool result turns).
func messagesFromHistory(history []ChatMessage) []provider.Message {
	out := make([]provider.Message, len(history))
	for i, m := range history {
		out[i] = provider.Message{
			Role:       provider.Role(m.Role),
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		}
	}
	return out
}

// chatEventPayload is the openclaw-shaped chat event payload.
type chatEventPayload struct {
	RunID        string             `json:"runId"`
	SessionKey   string             `json:"sessionKey"`
	Seq          int                `json:"seq"`
	State        string             `json:"state"`
	Message      *chatEventMessage  `json:"message,omitempty"`
	ErrorKind    string             `json:"errorKind,omitempty"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
}

type chatEventMessage struct {
	Phase   string                  `json:"phase"`
	Role    string                  `json:"role"`
	Content []chatEventContentPart  `json:"content"`
}

type chatEventContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (h *ChatHandler) emitChat(sess *Session, runID, sessionKey string, seq int, state, text string) error {
	payload := chatEventPayload{
		RunID:      runID,
		SessionKey: sessionKey,
		Seq:        seq,
		State:      state,
		Message: &chatEventMessage{
			Phase: "assistant",
			Role:  "assistant",
			Content: []chatEventContentPart{
				{Type: "text", Text: text},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sess.PushEvent(ctx, "chat", payload)
}

// agentEventPayload is the openclaw "agent" event envelope. The web UI's
// handleAgentEvent dispatches on payload.stream — for our tool execution
// surface the only one we emit is stream="tool".
type agentEventPayload struct {
	Stream     string         `json:"stream"`
	SessionKey string         `json:"sessionKey"`
	RunID      string         `json:"runId"`
	Ts         int64          `json:"ts"`
	Data       map[string]any `json:"data"`
}

// emitAgentToolStart fires before runner.Run so the UI's tool stream
// renders the in-flight tool card.
func (h *ChatHandler) emitAgentToolStart(sess *Session, runID, sessionKey, toolCallID, name, argumentsJSON string) {
	payload := buildToolStartPayload(runID, sessionKey, toolCallID, name, argumentsJSON, time.Now().UnixMilli())
	pushAgentEvent(sess, payload)
}

// emitAgentToolResult fires after runner.Run completes (whether or not
// the tool errored — failures are already captured into output as
// "ERROR: ...").
func (h *ChatHandler) emitAgentToolResult(sess *Session, runID, sessionKey, toolCallID, name, output string) {
	payload := buildToolResultPayload(runID, sessionKey, toolCallID, name, output, time.Now().UnixMilli())
	pushAgentEvent(sess, payload)
}

// buildToolStartPayload constructs the agent.tool start event. argumentsJSON
// is decoded into an object when possible (matches openclaw's data.args
// shape); a parse failure falls back to the raw string so the UI still has
// something to show.
func buildToolStartPayload(runID, sessionKey, toolCallID, name, argumentsJSON string, ts int64) agentEventPayload {
	var args any
	if argumentsJSON != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			args = argumentsJSON
		}
	}
	return agentEventPayload{
		Stream:     "tool",
		SessionKey: sessionKey,
		RunID:      runID,
		Ts:         ts,
		Data: map[string]any{
			"toolCallId": toolCallID,
			"name":       name,
			"phase":      "start",
			"args":       args,
		},
	}
}

func buildToolResultPayload(runID, sessionKey, toolCallID, name, output string, ts int64) agentEventPayload {
	return agentEventPayload{
		Stream:     "tool",
		SessionKey: sessionKey,
		RunID:      runID,
		Ts:         ts,
		Data: map[string]any{
			"toolCallId": toolCallID,
			"name":       name,
			"phase":      "result",
			"result":     output,
		},
	}
}

func pushAgentEvent(sess *Session, payload agentEventPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = sess.PushEvent(ctx, "agent", payload)
}

func (h *ChatHandler) emitError(sess *Session, runID, sessionKey string, seq int, kind, msg string) error {
	payload := chatEventPayload{
		RunID:        runID,
		SessionKey:   sessionKey,
		Seq:          seq,
		State:        "error",
		ErrorKind:    kind,
		ErrorMessage: msg,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sess.PushEvent(ctx, "chat", payload)
}

// AgentIDFromSessionKey parses a session-key into the agent it addresses.
// Three shapes are accepted:
//
//   - "agent:<agentId>:<conversationId>" → agentId   (canonical form)
//   - "agent:<agentId>"                  → agentId   (legacy short form)
//   - "<agentId>"                        → agentId   (bare; how the openclaw
//                                                    web UI passes the URL
//                                                    `?session=` param)
//
// Anything with a colon but no `agent:` prefix is rejected as ambiguous and
// returns "". An empty input also returns "".
//
// Exported because cmd/talon and tests need to derive agentIds the same way.
func AgentIDFromSessionKey(sessionKey string) string {
	if sessionKey == "" {
		return ""
	}
	const prefix = "agent:"
	if strings.HasPrefix(sessionKey, prefix) {
		rest := sessionKey[len(prefix):]
		if id, _, ok := strings.Cut(rest, ":"); ok {
			return id
		}
		return rest
	}
	// Bare form — accept only if it has no colon (otherwise it's some other
	// namespaced form we don't understand and shouldn't guess at).
	if strings.ContainsRune(sessionKey, ':') {
		return ""
	}
	return sessionKey
}

func newRunID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(b), nil
}

