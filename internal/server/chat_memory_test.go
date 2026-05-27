package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/guygrigsby/jess/memory"

	"github.com/guygrigsby/talon/internal/provider"
)

// stubEmbedder is a deterministic tiny vector producer that doesn't
// require a model download. Each lowercased token gets one slot;
// matching tokens contribute 1.0. Good enough to exercise jess's
// ChromemStore Append + SearchVector without pulling GoMLX.
type stubEmbedder struct {
	dim   int
	vocab map[string]int
}

func newStubEmbedder(vocab []string) *stubEmbedder {
	v := map[string]int{}
	for i, w := range vocab {
		v[w] = i
	}
	return &stubEmbedder{dim: len(vocab), vocab: v}
}

func (e *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	out := make([]float32, e.dim)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		if i, ok := e.vocab[tok]; ok {
			out[i] = 1
		}
	}
	return out, nil
}
func (e *stubEmbedder) Dim() int     { return e.dim }
func (e *stubEmbedder) Name() string { return "test:stub" }

func newTestMemory(t *testing.T) *MemoryConfig {
	t.Helper()
	emb := newStubEmbedder([]string{"tabs", "spaces", "feline", "kitten", "cat", "dog", "go"})
	store, err := memory.NewChromemStore(emb, memory.ChromemOptions{})
	if err != nil {
		t.Fatalf("ChromemStore: %v", err)
	}
	return &MemoryConfig{
		Store:    store,
		Recaller: memory.NewHybridRecaller(memory.NewVectorRecaller(), memory.NewSimpleRecaller()),
	}
}

func TestWithMemory_NilDisables(t *testing.T) {
	h := &ChatHandler{}
	h.WithMemory(nil)
	if h.memory != nil {
		t.Error("WithMemory(nil) should leave h.memory nil")
	}
}

func TestWithMemory_FillsDefaults(t *testing.T) {
	h := &ChatHandler{}
	h.WithMemory(&MemoryConfig{}) // store nil intentionally; defaults still apply
	if h.memory.Kinds == nil {
		t.Error("Kinds should default to a fresh KindRegistry")
	}
	if h.memory.MaxRecallEntries == 0 {
		t.Error("MaxRecallEntries should default to non-zero")
	}
	if h.memory.MemoryHeader == "" {
		t.Error("MemoryHeader should default to a non-empty string")
	}
}

func TestStampSourceCtx_AttachesProvenance(t *testing.T) {
	ctx := stampSourceCtx(context.Background(), "agent:main:main", "run-123")
	src := memory.SourceFromContext(ctx)
	if src.SessionID != "agent:main:main" {
		t.Errorf("SessionID = %q, want agent:main:main", src.SessionID)
	}
	if src.MessageID != "run-123" {
		t.Errorf("MessageID = %q, want run-123", src.MessageID)
	}
	if src.Tool != "remember" {
		t.Errorf("Tool = %q, want remember", src.Tool)
	}
}

// stubRunner: minimal ToolRunner that records what got dispatched.
type stubRunner struct {
	mu     sync.Mutex
	calls  []string
	specs  []provider.ToolSpec
	output map[string]string
}

func (s *stubRunner) Run(_ context.Context, name string, _ json.RawMessage) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
	if out, ok := s.output[name]; ok {
		return out, nil
	}
	return "ok:" + name, nil
}
func (s *stubRunner) Specs() []provider.ToolSpec { return s.specs }

func TestWrapWithRemember_AddsRememberToolSpec(t *testing.T) {
	mc := newTestMemory(t)
	remember := memory.NewRememberTool(mc.Store, memory.RememberOptions{AgentID: "main"})
	inner := &stubRunner{
		specs: []provider.ToolSpec{{Name: "bash", Description: "run a shell command"}},
	}
	wrapped := wrapWithMemoryTools(inner, remember, nil)
	specs := wrapped.Specs()
	if len(specs) != 2 {
		t.Fatalf("expected inner + remember = 2 specs, got %d", len(specs))
	}
	names := []string{specs[0].Name, specs[1].Name}
	if names[0] != "bash" || names[1] != "remember" {
		t.Errorf("specs in unexpected order/contents: %v", names)
	}
}

func TestWrapWithRemember_DispatchesByName(t *testing.T) {
	mc := newTestMemory(t)
	remember := memory.NewRememberTool(mc.Store, memory.RememberOptions{AgentID: "main"})
	inner := &stubRunner{output: map[string]string{"bash": "echo'd"}}
	wrapped := wrapWithMemoryTools(inner, remember, nil)

	// inner tool dispatches via the inner runner.
	got, err := wrapped.Run(context.Background(), "bash", json.RawMessage(`{"cmd":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo'd" {
		t.Errorf("inner tool result = %q, want echo'd", got)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "bash" {
		t.Errorf("inner should have seen the bash call; got %v", inner.calls)
	}

	// remember dispatches via the jess tool and lands an entry in
	// the store.
	_, err = wrapped.Run(context.Background(), "remember",
		json.RawMessage(`{"kind":"user","text":"user is a senior dev"}`))
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := mc.Store.Recall(context.Background(), memory.Query{AgentID: "main"}, 0)
	if len(got2) != 1 || !strings.Contains(got2[0].Text, "senior dev") {
		t.Errorf("remember should have landed the entry; got %v", got2)
	}
	// inner runner should NOT have seen the remember call.
	for _, c := range inner.calls {
		if c == "remember" {
			t.Error("inner runner should not see remember calls")
		}
	}
}

func TestWrapWithRemember_NilRememberPassesThrough(t *testing.T) {
	inner := &stubRunner{}
	got := wrapWithMemoryTools(inner, nil, nil)
	if got != inner {
		t.Error("nil remember should leave inner untouched (returned as-is)")
	}
}
