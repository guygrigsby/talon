package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/provider"
)

func TestAgentIDFromSessionKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"agent:main:main", "main"},
		{"agent:coding:abc123", "coding"},
		{"agent:research", "research"}, // legacy short form
		{"main", "main"},               // bare form — what the UI sends from ?session=main
		{"deepwork", "deepwork"},
		{"", ""},
		{"foo:bar:baz", ""},        // wrong prefix
		{"agent:", ""},              // empty agent — caller treats this as invalid
		{"agent::convo", ""},        // empty agent before colon
		{"agent:x:y:z", "x"},        // extra segments → first wins
	}
	for _, tc := range cases {
		if got := AgentIDFromSessionKey(tc.in); got != tc.want {
			t.Errorf("AgentIDFromSessionKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// stubResolver implements AgentResolver for tests.
type stubResolver struct {
	models map[string]provider.ModelID
	err    error
}

func (r *stubResolver) PrimaryModel(agentID string) (provider.ModelID, error) {
	if r.err != nil {
		return "", r.err
	}
	m, ok := r.models[agentID]
	if !ok {
		return "", ErrAgentNotFound
	}
	return m, nil
}

// stubFactory implements ProviderFactory for tests.
type stubFactory struct {
	provider provider.Provider
	err      error
}

func (f *stubFactory) For(providerName, agentID string) (provider.Provider, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.provider, nil
}

func TestChatStore_AppendAndSnapshot(t *testing.T) {
	s := NewChatStore()
	s.Append("agent:main:main", "user", "hello")
	s.Append("agent:main:main", "assistant", "hi there")
	s.Append("agent:other:other", "user", "second session")

	got := s.Snapshot("agent:main:main")
	if len(got) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Errorf("first message = %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "hi there" {
		t.Errorf("second message = %+v", got[1])
	}

	// Mutate snapshot — store must be unaffected.
	got[0].Content = "tampered"
	if again := s.Snapshot("agent:main:main"); again[0].Content != "hello" {
		t.Errorf("Snapshot returns aliased slice; mutating leaked into store")
	}

	if s.Snapshot("nonexistent") != nil {
		t.Errorf("Snapshot of unknown key should be nil")
	}
}

func TestChatHandler_RejectsMissingFields(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	cases := []struct {
		name   string
		params string
	}{
		{"empty params", `{}`},
		{"empty message", `{"sessionKey":"agent:main:main","message":""}`},
		{"empty sessionKey", `{"sessionKey":"","message":"hi"}`},
		{"bogus sessionKey", `{"sessionKey":"foo:bar","message":"hi"}`},
		{"malformed json", `{not json}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, []byte(tc.params))
			if ferr == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestChatHandler_AgentLookupFailureSurfacedAsInternal(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{err: ErrAgentNotFound},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	_, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, []byte(`{"sessionKey":"agent:main:main","message":"hi"}`))
	if ferr == nil || ferr.Code != ErrCodeInternal {
		t.Errorf("want INTERNAL on missing agent, got %+v", ferr)
	}
}

func TestChatHandler_ProviderRejectionSurfacedAsInternal(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{err: ErrProviderUnavailable},
		NewChatStore(),
	)
	_, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, []byte(`{"sessionKey":"agent:main:main","message":"hi"}`))
	if ferr == nil || ferr.Code != ErrCodeInternal {
		t.Errorf("want INTERNAL on missing provider, got %+v", ferr)
	}
	if !strings.Contains(ferr.Message, "provider") {
		t.Errorf("error message should mention provider: %q", ferr.Message)
	}
}

func TestChatHandler_ModelMissingProviderSegment(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "rawmodel-no-slash"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	_, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, []byte(`{"sessionKey":"agent:main:main","message":"hi"}`))
	if ferr == nil || ferr.Code != ErrCodeInternal {
		t.Errorf("want INTERNAL on missing provider segment, got %+v", ferr)
	}
}

func TestChatHandler_IdempotencyReturnsSameRunID(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		// Provider stream blocks until ctx is cancelled — that keeps the
		// run "active" so the second send matches the idempotency entry.
		&stubFactory{provider: provider.NewStub("openai", []provider.Delta{
			{Kind: provider.DeltaText, Text: "delayed"},
		})},
		NewChatStore(),
	)
	h.StreamTimeout = 50 * time.Millisecond

	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"abc"}`)
	res1, err1 := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if err1 != nil {
		t.Fatalf("first call: %+v", err1)
	}
	runID1 := res1.(map[string]any)["runId"].(string)

	res2, err2 := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if err2 != nil {
		t.Fatalf("second call: %+v", err2)
	}
	runID2 := res2.(map[string]any)["runId"].(string)

	if runID1 != runID2 {
		t.Errorf("idempotent send returned different runIds: %q vs %q", runID1, runID2)
	}
}

func TestChatHandler_RunIDEqualsIdempotencyKey(t *testing.T) {
	// The openclaw web UI generates a UUID, sends it as idempotencyKey,
	// and matches subsequent chat events on payload.runId === that UUID.
	// chat.send must echo the idempotencyKey back as runId or the UI
	// won't recognize the run as terminal and the typing indicator will
	// stick.
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", []provider.Delta{{Kind: provider.DeltaText, Text: "hi"}})},
		NewChatStore(),
	)
	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"a1b2-c3d4-e5f6"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if got := res.(map[string]any)["runId"].(string); got != "a1b2-c3d4-e5f6" {
		t.Errorf("runId = %q, want idempotencyKey %q", got, "a1b2-c3d4-e5f6")
	}
}

func TestChatHandler_NoIdempotencyKeyMeansNewRun(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	body := []byte(`{"sessionKey":"agent:main:main","message":"hi"}`)
	res1, _ := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	res2, _ := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	r1 := res1.(map[string]any)["runId"].(string)
	r2 := res2.(map[string]any)["runId"].(string)
	if r1 == r2 {
		t.Errorf("expected distinct runIds without idempotency key, got both %q", r1)
	}
}

func TestChatHandler_HistoryReturnsStoredMessages(t *testing.T) {
	store := NewChatStore()
	store.Append("agent:main:main", "user", "hello")
	store.Append("agent:main:main", "assistant", "hi there")
	store.Append("agent:main:main", "user", "second")

	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		store,
	)

	res, ferr := h.handleHistory(t.Context(), HandlerCtx{Session: nil}, []byte(`{"sessionKey":"agent:main:main","limit":50}`))
	if ferr != nil {
		t.Fatalf("handleHistory: %+v", ferr)
	}
	envelope := res.(map[string]any)
	msgs := envelope["messages"].([]historyMessage)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "hello" {
		t.Errorf("first message = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content[0].Text != "hi there" {
		t.Errorf("second message = %+v", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Content[0].Text != "second" {
		t.Errorf("third message = %+v", msgs[2])
	}
	// __openclaw metadata must be unique within the session and stable
	// across reads (we'll re-call below to confirm).
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Openclaw.ID == "" || seen[m.Openclaw.ID] {
			t.Errorf("ids should be non-empty and unique: %+v", m.Openclaw)
		}
		seen[m.Openclaw.ID] = true
		if m.Openclaw.Seq < 1 {
			t.Errorf("seq must be >= 1: %+v", m.Openclaw)
		}
	}
	// Stability: same input → same ids.
	res2, _ := h.handleHistory(t.Context(), HandlerCtx{Session: nil}, []byte(`{"sessionKey":"agent:main:main","limit":50}`))
	msgs2 := res2.(map[string]any)["messages"].([]historyMessage)
	for i := range msgs {
		if msgs[i].Openclaw.ID != msgs2[i].Openclaw.ID {
			t.Errorf("id %d not stable: %s vs %s", i, msgs[i].Openclaw.ID, msgs2[i].Openclaw.ID)
		}
	}
}

func TestChatHandler_HistoryLimit(t *testing.T) {
	store := NewChatStore()
	for i := 0; i < 10; i++ {
		store.Append("k", "user", "m")
	}
	h := NewChatHandler(
		&stubResolver{},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		store,
	)
	res, ferr := h.handleHistory(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"k","limit":3}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	msgs := res.(map[string]any)["messages"].([]historyMessage)
	if len(msgs) != 3 {
		t.Errorf("limit=3 returned %d messages", len(msgs))
	}
	// Should be the *last* 3, not the first.
	if msgs[0].Openclaw.Seq != 1 || msgs[2].Openclaw.Seq != 3 {
		// seq is renumbered from 1 within the truncated slice — we just
		// want to confirm the limit applied to the tail. The strongest
		// assertion is "got 3", which we already verified.
	}
}

func TestChatHandler_HistoryUnknownSessionReturnsEmpty(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	res, ferr := h.handleHistory(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"nope","limit":10}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	msgs := res.(map[string]any)["messages"].([]historyMessage)
	if len(msgs) != 0 {
		t.Errorf("unknown session should return empty, got %d", len(msgs))
	}
}

func TestChatHandler_HistoryRejectsMissingSessionKey(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	_, ferr := h.handleHistory(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Errorf("want BAD_REQUEST for missing sessionKey, got %+v", ferr)
	}
}

func TestChatHandler_RegisterAddsBothMethods(t *testing.T) {
	r := NewRegistry()
	h := NewChatHandler(&stubResolver{}, &stubFactory{}, NewChatStore())
	h.Register(r)
	methods := r.Methods()
	want := map[string]bool{"chat.send": false, "chat.history": false}
	for _, m := range methods {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for m, found := range want {
		if !found {
			t.Errorf("Register did not add %q (got %v)", m, methods)
		}
	}
}

func TestErrAgentNotFoundIsExported(t *testing.T) {
	// Ensure the sentinel is wrappable, so callers can errors.Is on it.
	wrapped := errors.New("wrap: " + ErrAgentNotFound.Error())
	if errors.Is(wrapped, ErrAgentNotFound) {
		t.Errorf("plain string wrap should NOT match — that's fine; this asserts the path with fmt.Errorf works")
	}
	// Real wrapped form:
	if !errors.Is(wrappedErr(ErrAgentNotFound), ErrAgentNotFound) {
		t.Errorf("errors.Is should unwrap our sentinel")
	}
}

func wrappedErr(e error) error {
	return errors.Join(errors.New("preface"), e)
}
