package server

// Memory sidecar — wires jess/memory into ChatHandler without
// touching the loop or the WebSocket contract. Two augmentations
// happen per chat.send turn:
//
//  1. The system prompt gets prepended with a "memories" block:
//     entries from AlwaysInclude Kinds (user, feedback) on every
//     turn; recall-only Kinds (project, reference) when relevant
//     to the running conversation hint.
//  2. The RememberTool joins the per-loop tool registry so the
//     model can save new memories during a turn.
//
// Both are noop when no jess/memory Store has been configured —
// chats without a wired memory store behave exactly as before.
//
// jess's agentcore.ContextManager isn't used here on purpose; it's
// designed for the agentcore loop and would require converting
// talon's provider.Message history to agentcore.AgentMessage and
// back. The recall + format logic is small; we inline it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// augmentSystemPrompt prepends a layered memories block to the
// existing system prompt when a Store is configured. Layered like
// jess's ContextManager output:
//
//   - "Core memories (always relevant):" — AlwaysInclude Kinds
//     fetched directly from the store, capped per-Kind by policy.
//   - "Relevant memories for this conversation:" — non-AlwaysInclude
//     Kinds, scored by the recaller against the conversation hint.
//
// Failures here MUST NOT abort the chat — a broken memory layer
// should degrade to no-memory, not no-agent. Errors get swallowed
// (TODO: slog.Debug them once we have a logger handle here).
func (h *ChatHandler) augmentSystemPrompt(ctx context.Context, base, agentID, conversationHint string) string {
	if h.memory == nil || h.memory.Store == nil {
		return base
	}
	core := h.coreMemories(ctx, agentID)
	relevant := h.relevantMemories(ctx, agentID, conversationHint, len(core))
	if len(core) == 0 && len(relevant) == 0 {
		return base
	}
	block := h.formatMemoryBlock(core, relevant)
	if base == "" {
		return block
	}
	return block + "\n\n" + base
}

// coreMemories pulls AlwaysInclude entries straight from the store.
// Per-Kind policy caps the count. Order across Kinds is registry
// order; within a Kind it's newest-first (Store.Recall semantics).
func (h *ChatHandler) coreMemories(ctx context.Context, agentID string) []memory.Entry {
	var out []memory.Entry
	for _, kind := range h.memory.Kinds.AlwaysIncludeKinds() {
		policy := h.memory.Kinds.PolicyFor(kind)
		max := policy.MaxEntries
		if max == 0 {
			max = h.memory.MaxRecallEntries
		}
		entries, err := h.memory.Store.Recall(ctx, memory.Query{
			AgentID: agentID,
			Kind:    string(kind),
		}, max)
		if err != nil {
			continue
		}
		out = append(out, entries...)
	}
	return out
}

// relevantMemories runs the recaller for the remaining budget,
// then drops any AlwaysInclude-kind entries that already surfaced
// via coreMemories (avoids duplicates when SimpleRecaller doesn't
// filter by Kind).
func (h *ChatHandler) relevantMemories(ctx context.Context, agentID, hint string, alreadyCore int) []memory.Entry {
	budget := h.memory.MaxRecallEntries - alreadyCore
	if budget <= 0 {
		return nil
	}
	entries, err := h.memory.Recaller.Recall(ctx, h.memory.Store, agentID, hint, budget)
	if err != nil {
		return nil
	}
	out := entries[:0]
	for _, e := range entries {
		if h.memory.Kinds.PolicyFor(memory.Kind(e.Kind)).AlwaysInclude {
			continue
		}
		out = append(out, e)
	}
	return out
}

// formatMemoryBlock renders the two layers as a single text block
// to prepend to the system prompt. Markdown-ish; matches jess's
// own ContextManager output so the model sees a consistent shape
// regardless of which transport surface delivered it.
func (h *ChatHandler) formatMemoryBlock(core, relevant []memory.Entry) string {
	var b strings.Builder
	if len(core) > 0 {
		b.WriteString("Core memories (always relevant):\n\n")
		writeMemoryEntries(&b, core)
		if len(relevant) > 0 {
			b.WriteByte('\n')
		}
	}
	if len(relevant) > 0 {
		b.WriteString(h.memory.MemoryHeader)
		b.WriteString("\n\n")
		writeMemoryEntries(&b, relevant)
	}
	return b.String()
}

func writeMemoryEntries(b *strings.Builder, entries []memory.Entry) {
	for _, e := range entries {
		b.WriteString("- ")
		if e.Kind != "" {
			b.WriteByte('[')
			b.WriteString(e.Kind)
			b.WriteString("] ")
		}
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
}

// lastUserText returns the trailing user message content from the
// run's history snapshot. Used as the conversation hint the
// recaller scores against. Empty when no user message has been
// recorded yet (first turn — recall falls back to AlwaysInclude
// only).
func lastUserText(history []ChatMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && strings.TrimSpace(history[i].Content) != "" {
			return history[i].Content
		}
	}
	return ""
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

// Specs returns the union of inner tools + memory tools. Memory
// tools appear last so they don't shadow same-named host tools in
// the model's tool index.
func (m *memoryAugmentedRunner) Specs() []provider.ToolSpec {
	var out []provider.ToolSpec
	if m.inner != nil {
		out = m.inner.Specs()
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

// silence unused-import shenanigans when only some helpers compile
// (time is used for future extension hooks).
var _ = time.Now
