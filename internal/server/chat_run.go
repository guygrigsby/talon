// chat_run.go — goroutine driver for the jess-backed chat runner.
//
// Routing is automatic: if cmd/talon injects a ChatRunFn at gateway
// construction, chat.send uses the jess-backed chat driver for every
// provider.
//
// cmd/talon injects the ChatRunFn at gateway construction so
// internal/server doesn't take a transitive dep on the chat driver.

package server

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guygrigsby/talon/internal/audit"
)

// resolveModelForRouting picks the model the chat-send will use,
// honoring the same per-session override the direct provider path does so
// routing matches what handleSend would actually call. Returns
// the full "<provider>/<model>" string.
func (h *ChatHandler) resolveModelForRouting(agentID, sessionKey string) (modelStringer, error) {
	model, err := h.resolver.PrimaryModel(agentID)
	if err != nil {
		return nil, err
	}
	if h.sessions != nil {
		if override := h.sessions.Model(sessionKey); override != "" {
			return providerModelID(override), nil
		}
	}
	return providerModelID(string(model)), nil
}

// modelStringer is the lookup contract the dispatch helper needs:
// just the model id as a string (provider/segment included).
type modelStringer interface {
	String() string
}

// providerModelID is a thin wrapper exposing Provider() + String()
// on a string model id.
type providerModelID string

func (m providerModelID) String() string { return string(m) }
func (m providerModelID) Provider() string {
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			return string(m[:i])
		}
	}
	return ""
}

// shouldUseChatRunnerFor reports whether this handler can route a
// model through the jess-backed chat runner. The model id is accepted
// for the call-site's benefit, but provider-specific routing
// exceptions live below the chat driver now; Talon no longer keeps a
// direct-provider bypass.
func (h *ChatHandler) shouldUseChatRunnerFor(modelID string) bool {
	_ = modelID
	return h.chatRun != nil
}

// handleSendViaChatRunner is the jess-backed chat driver entry point.
// Same public contract as handleSend (returns {runId} immediately,
// runs streaming in a goroutine).
//
// Differences from the legacy runStream:
//   - Provider/model construction lives in the chat driver; this
//     layer only forwards the per-session model override.
//   - No tool/runner wiring at this level — the chat driver builds
//     its own tool set.
//   - History flows through ChatStore the same way (user message
//     appended pre-stream; assistant message appended post-stream
//     by the emit-final hook).
//
// Cost cap and sessions still apply: the cap check ran in
// handleSend; per-session model override is read here so the UI's
// picker continues to drive the model choice.
func (h *ChatHandler) handleSendViaChatRunner(ctx context.Context, p chatSendParams, agentID string) (any, *FrameError) {
	runID := p.IdempotencyKey
	if runID == "" {
		newID, err := newRunID()
		if err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: "chat.send: " + err.Error()}
		}
		runID = newID
	}

	runKey := p.SessionKey + "|" + runID
	h.runsMu.Lock()
	if _, ok := h.runs[runKey]; ok {
		h.runsMu.Unlock()
		return map[string]any{"runId": runID}, nil
	}
	h.runs[runKey] = runID
	h.runsMu.Unlock()

	priorHistory := h.store.Snapshot(p.SessionKey)
	h.store.Append(p.SessionKey, "user", p.Message)

	go h.runChatStream(runID, p.SessionKey, agentID, p.Message, priorHistory, runKey)

	return map[string]any{"runId": runID}, nil
}

// runChatStream is the goroutine driver for the jess-backed chat
// runner path. Mirrors the legacy runStream's runs-map cleanup +
// StreamTimeout scaffolding, then delegates the actual prompt to the
// injected ChatRunFn.
func (h *ChatHandler) runChatStream(runID, sessionKey, agentID, userText string, priorHistory []ChatMessage, runKey string) {
	defer func() {
		if runKey != "" {
			h.runsMu.Lock()
			delete(h.runs, runKey)
			h.runsMu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), h.StreamTimeout)
	defer cancel()

	selectedModelID := ""
	if h.sessions != nil {
		selectedModelID = h.sessions.Model(sessionKey)
	}

	// Lifecycle INFO: one line per turn boundary, correlated by
	// session/run/agent. Per-token output stays on the event stream.
	turnStart := time.Now()
	slog.Info("chat turn start",
		"session", sessionKey, "run", runID, "agent", agentID, "model", selectedModelID)
	defer func() {
		slog.Info("chat turn end",
			"session", sessionKey, "run", runID, "dur", time.Since(turnStart))
	}()

	if h.audit != nil {
		auditModel := selectedModelID
		if auditModel == "" && h.resolver != nil {
			if m, err := h.resolveModelForRouting(agentID, sessionKey); err == nil && m != nil {
				auditModel = m.String()
			}
		}
		h.recordAudit(audit.Event{
			Kind:    audit.KindTurnStart,
			Session: sessionKey,
			Run:     runID,
			Agent:   agentID,
			Model:   auditModel,
		})
		defer func() {
			h.recordAudit(audit.Event{
				Kind:    audit.KindTurnEnd,
				Session: sessionKey,
				Run:     runID,
				Agent:   agentID,
			})
			// Drop the per-run audit seq counter so the map doesn't grow
			// unbounded across the gateway's lifetime.
			h.auditSeqMu.Lock()
			delete(h.auditSeq, runID)
			h.auditSeqMu.Unlock()
		}()
	}

	var seq atomic.Int64
	var emitMu sync.Mutex
	emitText := func(s int, state, full, delta string) {
		emitMu.Lock()
		defer emitMu.Unlock()
		_ = h.emitChat(sessionKey, runID, sessionKey, s, state, full, delta)
	}
	emitToolStart := func(id, name, args string) {
		// DEBUG per-tool dispatch; args stay off the log line (they
		// may carry secrets) and remain on the audit/event stream.
		slog.Debug("tool dispatch", "tool", name, "session", sessionKey, "run", runID)
		h.emitAgentToolStart(sessionKey, runID, sessionKey, id, name, args)
	}
	emitToolResult := func(id, name, out string, isErr bool) {
		h.emitAgentToolResult(sessionKey, runID, sessionKey, id, name, out, isErr)
	}
	emitError := func(s int, kind, msg string) {
		_ = h.emitError(sessionKey, runID, sessionKey, s, kind, msg)
	}

	// Wrap emitText to auto-increment seq so the injected runner
	// doesn't need to track ordering.
	wrappedEmitText := func(state, full, delta string) {
		s := int(seq.Add(1))
		emitText(s, state, full, delta)
	}
	wrappedEmitError := func(kind, msg string) {
		s := int(seq.Add(1))
		emitError(s, kind, msg)
	}

	result, err := h.chatRun(
		ctx,
		agentID, sessionKey, runID, userText, selectedModelID,
		priorHistory,
		func(_ int, state, full, delta string) {
			wrappedEmitText(state, full, delta)
		},
		emitToolStart,
		emitToolResult,
		func(_ int, kind, msg string) {
			wrappedEmitError(kind, msg)
		},
	)

	if err != nil {
		// Error already emitted by the runner; just record in the
		// store so the next turn's history sees nothing partial.
		return
	}
	h.recordChatUsage(agentID, sessionKey, result)
	if result.FinalText != "" {
		h.store.Append(sessionKey, "assistant", result.FinalText)
	}
}

func (h *ChatHandler) recordChatUsage(agentID, sessionKey string, result ChatRunResult) {
	if h.costs == nil || result.Usage.isZero() {
		return
	}
	modelID := result.ModelID
	if modelID == "" {
		model, err := h.resolveModelForRouting(agentID, sessionKey)
		if err != nil {
			return
		}
		modelID = model.String()
	}
	h.costs.RecordUsage(agentID, modelID, result.Usage.InputTokens, result.Usage.OutputTokens)
}
