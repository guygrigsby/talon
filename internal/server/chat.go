package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/provider"
)

// AgentResolver looks up the model an agent should drive a chat with. The
// gateway's chat.send handler calls this once per send to translate the
// agentId (parsed from sessionKey) into a concrete ModelID. Returns
// ErrAgentNotFound when the agent does not exist.
type AgentResolver interface {
	PrimaryModel(agentID string) (provider.ModelID, error)
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
// addressed by a sessionKey. The chat.send handler appends both the user
// turn and the final assistant turn here. Messages do not persist across
// gateway restarts in this MVP (talon-2dl will persist if needed).
type ChatStore struct {
	mu      sync.Mutex
	history map[string][]ChatMessage
}

// ChatMessage is a single turn stored in the history.
type ChatMessage struct {
	Role    string
	Content string
	At      time.Time
}

// NewChatStore returns an empty in-memory store.
func NewChatStore() *ChatStore {
	return &ChatStore{history: make(map[string][]ChatMessage)}
}

// Append adds a message to sessionKey's history.
func (s *ChatStore) Append(sessionKey, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[sessionKey] = append(s.history[sessionKey], ChatMessage{Role: role, Content: content, At: time.Now()})
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

// ChatHandler is the chat.send registry handler. Construct one with
// NewChatHandler, then register it on the server's Registry.
type ChatHandler struct {
	resolver AgentResolver
	factory  ProviderFactory
	store    *ChatStore

	// runs tracks active runs by "sessionKey|idempotencyKey" so a duplicate
	// chat.send returns the same runId without spawning a second stream.
	runsMu sync.Mutex
	runs   map[string]string

	// streamTimeout caps a single chat.send stream. Default 5 minutes;
	// override in tests via the StreamTimeout field.
	StreamTimeout time.Duration
}

// NewChatHandler constructs a ChatHandler that uses resolver to look up
// agent models, factory to materialize providers, and store to record
// history. All three are required; nil values will cause the handler to
// reject sends with INTERNAL errors.
func NewChatHandler(resolver AgentResolver, factory ProviderFactory, store *ChatStore) *ChatHandler {
	return &ChatHandler{
		resolver:      resolver,
		factory:       factory,
		store:         store,
		runs:          make(map[string]string),
		StreamTimeout: 5 * time.Minute,
	}
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

// historyMessage is the per-message envelope chat.history emits. Mirrors the
// fields openclaw's web UI consumes; openclaw decorates each row with
// __openclaw.id (a stable React key) and __openclaw.seq.
type historyMessage struct {
	Openclaw  openclawMeta           `json:"__openclaw"`
	Role      string                 `json:"role"`
	Content   []chatEventContentPart `json:"content"`
	Timestamp int64                  `json:"timestamp"`
}

type openclawMeta struct {
	ID  string `json:"id"`
	Seq int    `json:"seq"`
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

	out := make([]historyMessage, len(msgs))
	for i, m := range msgs {
		out[i] = historyMessage{
			Openclaw:  openclawMeta{ID: messageID(p.SessionKey, i), Seq: i + 1},
			Role:      m.Role,
			Content:   []chatEventContentPart{{Type: "text", Text: m.Content}},
			Timestamp: m.At.UnixMilli(),
		}
	}
	return map[string]any{"messages": out}, nil
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
	providerName := model.Provider()
	if providerName == "" {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: agent's primary model is missing a provider segment: " + string(model)}
	}

	prov, err := h.factory.For(providerName, agentID)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: provider: " + err.Error()}
	}

	// Idempotency: same {sessionKey, idempotencyKey} returns the existing
	// runId without spawning a second stream. We don't replay events to
	// late subscribers in this MVP — the openclaw UI re-sends after a
	// reconnect with a fresh idempotencyKey so this is acceptable.
	runKey := p.SessionKey + "|" + p.IdempotencyKey
	if p.IdempotencyKey != "" {
		h.runsMu.Lock()
		if existing, ok := h.runs[runKey]; ok {
			h.runsMu.Unlock()
			return map[string]any{"runId": existing}, nil
		}
		h.runsMu.Unlock()
	}

	runID, err := newRunID()
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: " + err.Error()}
	}
	if p.IdempotencyKey != "" {
		h.runsMu.Lock()
		h.runs[runKey] = runID
		h.runsMu.Unlock()
	}

	h.store.Append(p.SessionKey, "user", p.Message)
	history := h.store.Snapshot(p.SessionKey)
	reqMsgs := make([]provider.Message, 0, len(history))
	for _, m := range history {
		reqMsgs = append(reqMsgs, provider.Message{Role: provider.Role(m.Role), Content: m.Content})
	}

	go h.runStream(hc.Session, runID, p.SessionKey, prov, model, reqMsgs, runKey)

	return map[string]any{"runId": runID}, nil
}

func (h *ChatHandler) runStream(sess *Session, runID, sessionKey string, prov provider.Provider, model provider.ModelID, msgs []provider.Message, runKey string) {
	defer func() {
		if runKey != "" {
			h.runsMu.Lock()
			delete(h.runs, runKey)
			h.runsMu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), h.StreamTimeout)
	defer cancel()

	deltaCh, err := prov.Stream(ctx, provider.Request{Model: model, Messages: msgs})
	if err != nil {
		_ = h.emitError(sess, runID, sessionKey, 1, "provider", err.Error())
		return
	}

	var accumulated strings.Builder
	seq := 0
	emitFailures := 0
	for d := range deltaCh {
		switch d.Kind {
		case provider.DeltaText:
			accumulated.WriteString(d.Text)
			seq++
			if err := h.emitChat(sess, runID, sessionKey, seq, "streaming", accumulated.String()); err != nil {
				emitFailures++
				if emitFailures >= 3 {
					// Client is probably gone — stop streaming, but drain
					// the provider channel so its goroutine exits.
					cancel()
					for range deltaCh {
					}
					return
				}
			}
		case provider.DeltaError:
			seq++
			_ = h.emitError(sess, runID, sessionKey, seq, "provider", d.Err.Error())
			// Provider closes channel after DeltaError — loop will exit.
		case provider.DeltaUsage:
			// MVP: usage is not surfaced over the wire yet. Logged only.
			// Keep the case to avoid future-warning.
		}
	}

	final := accumulated.String()
	if final != "" {
		h.store.Append(sessionKey, "assistant", final)
	}
	seq++
	_ = h.emitChat(sess, runID, sessionKey, seq, "final", final)
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

