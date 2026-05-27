// Phase 3 of docs/migration-agentcore.md — gateway wiring for the
// agentcore-based chat handler in internal/agentcore_chat.
//
// Routing is automatic: if cmd/talon injects an AgentcoreRunFn at
// gateway construction, chat.send uses agentcore for every provider.
//
// cmd/talon injects the AgentcoreRunFn at gateway construction so
// internal/server doesn't take a direct dependency on agentcore.

package server

import (
	"context"
	"sync"
	"sync/atomic"
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

// shouldUseAgentcoreFor reports whether this handler can route a
// model through the agentcore path. The model id is accepted for the
// call-site's benefit, but provider-specific routing exceptions live
// below agentcore now; Talon no longer keeps a direct-provider bypass.
func (h *ChatHandler) shouldUseAgentcoreFor(modelID string) bool {
	_ = modelID
	return h.agentcoreRun != nil
}

// handleSendViaAgentcore is the agentcore-dispatch entry. Same
// public contract as handleSend (returns {runId} immediately, runs
// streaming in a goroutine).
//
// Differences from the legacy runStream:
//   - Provider/model construction lives in agentcore_chat; this
//     layer only forwards the per-session model override.
//   - No tool/runner wiring at this level — agentcore_chat builds
//     its own tool set.
//   - History flows through ChatStore the same way (user message
//     appended pre-stream; assistant message appended post-stream
//     by the emit-final hook).
//
// Cost cap and sessions still apply: the cap check ran in
// handleSend; per-session model override is read here so the UI's
// picker continues to drive the model choice.
func (h *ChatHandler) handleSendViaAgentcore(ctx context.Context, p chatSendParams, agentID string) (any, *FrameError) {
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

	go h.runStreamAgentcore(runID, p.SessionKey, agentID, p.Message, priorHistory, runKey)

	return map[string]any{"runId": runID}, nil
}

// runStreamAgentcore is the goroutine driver for the agentcore
// path. Mirrors runStream's runs-map cleanup + StreamTimeout
// scaffolding, then delegates the actual prompt to the injected
// AgentcoreRunFn.
func (h *ChatHandler) runStreamAgentcore(runID, sessionKey, agentID, userText string, priorHistory []ChatMessage, runKey string) {
	defer func() {
		if runKey != "" {
			h.runsMu.Lock()
			delete(h.runs, runKey)
			h.runsMu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), h.StreamTimeout)
	defer cancel()

	var seq atomic.Int64
	var emitMu sync.Mutex
	emitText := func(s int, state, full, delta string) {
		emitMu.Lock()
		defer emitMu.Unlock()
		_ = h.emitChat(sessionKey, runID, sessionKey, s, state, full, delta)
	}
	emitToolStart := func(id, name, args string) {
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

	selectedModelID := ""
	if h.sessions != nil {
		selectedModelID = h.sessions.Model(sessionKey)
	}

	result, err := h.agentcoreRun(
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
	h.recordAgentcoreUsage(agentID, sessionKey, result)
	if result.FinalText != "" {
		h.store.Append(sessionKey, "assistant", result.FinalText)
	}
}

func (h *ChatHandler) recordAgentcoreUsage(agentID, sessionKey string, result AgentcoreRunResult) {
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
