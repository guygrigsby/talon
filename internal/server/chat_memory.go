package server

// Memory sidecar — carries the jess/memory pieces shared by the
// agentcore path and the temporary legacy provider loop.
//
// Prompt memory assembly belongs to jess's ContextManager, wired in
// internal/agentcore_chat. This file intentionally keeps only the
// small adapter the old direct provider loop needs until that loop
// is deleted: expose RememberTool/RecallTool as provider-shaped
// ToolRunner specs and stamp remember writes with Talon provenance.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/talon/internal/provider"
)

// MemoryConfig bundles the jess pieces ChatHandler uses for the
// sidecar. Optional — when nil, memory augmentation is skipped.
type MemoryConfig struct {
	// Store is the memory backend (typically a ChromemStore).
	Store memory.Store
	// Recaller selects entries to inject. Typically a
	// HybridRecaller wrapping VectorRecaller + SimpleRecaller.
	Recaller memory.Recaller
	// Kinds, when non-nil, overrides DefaultKindPolicies for the
	// always-include vs recall split. nil uses jess's defaults.
	Kinds *memory.KindRegistry
	// MaxRecallEntries caps how many recall-eligible entries get
	// injected per turn. 0 uses jess's default (8). Independent
	// of the always-include cap (which is per-Kind via the
	// KindRegistry policy).
	MaxRecallEntries int
	// MemoryHeader is the prefix line for the relevant-memories
	// block. Empty defaults to "Relevant memories for this
	// conversation:".
	MemoryHeader string
}

// WithMemory wires a jess/memory Store + Recaller into the chat
// handler. Returns the handler so the call can chain like the
// other With* options. nil mc disables memory augmentation
// (equivalent to never calling WithMemory).
func (h *ChatHandler) WithMemory(mc *MemoryConfig) *ChatHandler {
	if mc == nil {
		h.memory = nil
		return h
	}
	if mc.Kinds == nil {
		mc.Kinds = memory.NewKindRegistry()
	}
	if mc.MaxRecallEntries == 0 {
		mc.MaxRecallEntries = 8
	}
	if mc.MemoryHeader == "" {
		mc.MemoryHeader = "Relevant memories for this conversation:"
	}
	h.memory = mc
	return h
}

// memoryAugmentedRunner wraps the workspace ToolRunner with the
// jess RememberTool + RecallTool so the model can save AND query
// memory during a turn. Tool name dispatch: the inner runner gets
// first crack; "remember" / "recall" fall through to the jess
// tools here. Specs() reports the union (memory tools last so
// they don't shadow a same-named host tool).
type memoryAugmentedRunner struct {
	inner      ToolRunner
	remember   *memory.RememberTool
	recall     *memory.RecallTool
	rememberSp provider.ToolSpec
	recallSp   provider.ToolSpec
}

func (h *ChatHandler) wrapLegacyMemoryTools(ctx context.Context, emit emitTarget, agentID string, runner ToolRunner) (context.Context, ToolRunner) {
	if h.memory == nil || h.memory.Store == nil {
		return ctx, runner
	}
	ctx = stampSourceCtx(ctx, emit.sessionKey, emit.runID)
	remember := memory.NewRememberTool(h.memory.Store, memory.RememberOptions{
		AgentID: agentID,
	})
	var recall *memory.RecallTool
	if h.memory.Recaller != nil {
		recall = memory.NewRecallTool(h.memory.Store, h.memory.Recaller, memory.RecallOptions{
			AgentID: agentID,
		})
	}
	return ctx, wrapWithMemoryTools(runner, remember, recall)
}

// wrapWithMemoryTools returns a ToolRunner that handles "remember"
// + "recall" via jess and delegates everything else to inner.
// inner may be nil when no workspace tools are configured — the
// memory tools are still available. Either jess tool may be nil
// (host wired only one); the runner skips registration for
// whichever is missing.
func wrapWithMemoryTools(inner ToolRunner, remember *memory.RememberTool, recall *memory.RecallTool) ToolRunner {
	if remember == nil && recall == nil {
		return inner
	}
	m := &memoryAugmentedRunner{inner: inner, remember: remember, recall: recall}
	if remember != nil {
		schema, _ := json.Marshal(remember.Schema())
		m.rememberSp = provider.ToolSpec{
			Name:             remember.Name(),
			Description:      remember.Description(),
			ParametersSchema: schema,
		}
	}
	if recall != nil {
		schema, _ := json.Marshal(recall.Schema())
		m.recallSp = provider.ToolSpec{
			Name:             recall.Name(),
			Description:      recall.Description(),
			ParametersSchema: schema,
		}
	}
	return m
}

// Run dispatches by tool name. Memory tools win over inner so a
// host can't accidentally shadow them with a workspace tool of
// the same name; inner runner takes everything else.
func (m *memoryAugmentedRunner) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	if m.remember != nil && name == m.remember.Name() {
		out, err := m.remember.Execute(ctx, input)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	if m.recall != nil && name == m.recall.Name() {
		out, err := m.recall.Execute(ctx, input)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	if m.inner == nil {
		return "", fmt.Errorf("memoryAugmentedRunner: no inner runner for tool %q", name)
	}
	return m.inner.Run(ctx, name, input)
}

// Specs returns the union of inner tools + memory tools. Jess names
// (remember, recall) must not collide with anything the inner runner
// exposes — OpenAI-compat providers reject duplicate names with HTTP
// 400. Inner-runner collisions are dropped (jess wins), matching the
// Run-side dispatch order.
func (m *memoryAugmentedRunner) Specs() []provider.ToolSpec {
	skip := map[string]struct{}{}
	if m.remember != nil {
		skip[m.remember.Name()] = struct{}{}
	}
	if m.recall != nil {
		skip[m.recall.Name()] = struct{}{}
	}
	var out []provider.ToolSpec
	if m.inner != nil {
		for _, s := range m.inner.Specs() {
			if _, dup := skip[s.Name]; dup {
				continue
			}
			out = append(out, s)
		}
	}
	if m.remember != nil {
		out = append(out, m.rememberSp)
	}
	if m.recall != nil {
		out = append(out, m.recallSp)
	}
	return out
}

// stampSourceCtx threads jess's memory.WithSource into ctx so the
// RememberTool can stamp saved entries with the originating
// session + message. Called once per chat.send before tool
// dispatch starts.
func stampSourceCtx(ctx context.Context, sessionKey, runID string) context.Context {
	return memory.WithSource(ctx, memory.Source{
		SessionID: sessionKey,
		MessageID: runID,
		Tool:      "remember",
		Reason:    "model decided",
	})
}
