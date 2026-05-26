// Phase 3 of docs/migration-agentcore.md — gateway wiring for the
// agentcore-based chat handler in internal/agentcore_chat.
//
// Routing is automatic per-provider, no user-facing config flag:
//
//   - anthropic → legacy chat loop. agentcore+LiteLLM currently
//     400s on anthropic (top_p / temperature conflict, upstream
//     blocker). Legacy keeps working via the anthropic plugin
//     until the upstream patch lands.
//   - openai → legacy chat loop. The legacy OpenAI-compatible
//     provider is the known-good path for both Responses-era GPT-5
//     models and chat-completions models such as gpt-4o-mini.
//   - everything else → agentcore. deepseek, mistral, mlx,
//     lmstudio, ollama, plus any other provider that fits the
//     agentcore/llm path.
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
// honoring the same per-session override the legacy path does so
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

// shouldUseAgentcoreFor reports whether the named provider routes
// through the agentcore path. OpenAI stays on the legacy provider
// path for now: GPT-5 requires Responses API support, and the live
// gpt-4o-mini path has shown assistant turns disappearing under
// agentcore while the legacy plugin path continues to respond.
//
// Always false when the agentcore runner wasn't wired — that
// keeps fresh dev builds (without the cmd/talon hookup) on the
// legacy path.
func (h *ChatHandler) shouldUseAgentcoreFor(modelID string) bool {
	if h.agentcoreRun == nil {
		return false
	}
	providerName := providerModelID(modelID).Provider()

	// Anthropic stays on legacy until upstream LiteLLM ships the
	// top_p / temperature conflict fix. See
	// docs/migration-agentcore.md Phase 4 status.
	if providerName == "anthropic" {
		return false
	}
	if providerName == "openai" {
		return false
	}
	return true
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

	h.store.Append(p.SessionKey, "user", p.Message)

	go h.runStreamAgentcore(runID, p.SessionKey, agentID, p.Message, runKey)

	return map[string]any{"runId": runID}, nil
}

// runStreamAgentcore is the goroutine driver for the agentcore
// path. Mirrors runStream's runs-map cleanup + StreamTimeout
// scaffolding, then delegates the actual prompt to the injected
// AgentcoreRunFn.
func (h *ChatHandler) runStreamAgentcore(runID, sessionKey, agentID, userText, runKey string) {
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
		h.emitAgentToolResult(sessionKey, runID, sessionKey, id, name, out)
		_ = isErr // legacy emit doesn't carry an is-error flag yet; tracked for later
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

	modelOverride := ""
	if h.sessions != nil {
		modelOverride = h.sessions.Model(sessionKey)
	}

	finalText, err := h.agentcoreRun(
		ctx,
		agentID, sessionKey, runID, userText, modelOverride,
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
	if finalText != "" {
		h.store.Append(sessionKey, "assistant", finalText)
	}
}
