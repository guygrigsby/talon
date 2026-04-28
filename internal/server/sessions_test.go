package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/provider"
)

// --- store -----------------------------------------------------------------

func TestSessionStore_PatchAndGet(t *testing.T) {
	s := NewSessionStore()
	s.Patch("k", map[string]json.RawMessage{
		"model":         json.RawMessage(`"openai/gpt-4o"`),
		"thinkingLevel": json.RawMessage(`"high"`),
	})
	got, ok := s.Get("k")
	if !ok {
		t.Fatal("expected entry under k")
	}
	if string(got.Fields["model"]) != `"openai/gpt-4o"` {
		t.Errorf("model field = %s", got.Fields["model"])
	}
	if got.UpdatedAt == 0 {
		t.Errorf("updatedAt should be set")
	}
}

func TestSessionStore_PatchNullClearsField(t *testing.T) {
	s := NewSessionStore()
	s.Patch("k", map[string]json.RawMessage{"model": json.RawMessage(`"openai/x"`)})
	s.Patch("k", map[string]json.RawMessage{"model": json.RawMessage(`null`)})
	if got, _ := s.Get("k"); got.Fields["model"] != nil {
		t.Errorf("null should have cleared model field, got %s", got.Fields["model"])
	}
}

func TestSessionStore_ModelHelper(t *testing.T) {
	s := NewSessionStore()
	if s.Model("k") != "" {
		t.Errorf("unset key should yield empty string")
	}
	s.Patch("k", map[string]json.RawMessage{"model": json.RawMessage(`"deepseek/deepseek-reasoner"`)})
	if got := s.Model("k"); got != "deepseek/deepseek-reasoner" {
		t.Errorf("Model = %q", got)
	}
	// null clears.
	s.Patch("k", map[string]json.RawMessage{"model": json.RawMessage(`null`)})
	if got := s.Model("k"); got != "" {
		t.Errorf("after null patch, Model should be empty, got %q", got)
	}
}

func TestSessionStore_SnapshotIsACopy(t *testing.T) {
	s := NewSessionStore()
	s.Patch("k", map[string]json.RawMessage{"model": json.RawMessage(`"a"`)})
	snap := s.Snapshot()
	// Mutating the snapshot must not affect the store.
	snap["k"].Fields["model"] = json.RawMessage(`"tampered"`)
	if got := s.Model("k"); got != "a" {
		t.Errorf("snapshot mutation leaked into store: %q", got)
	}
}

// --- sessions.patch handler ----------------------------------------------

func TestSessionsPatch_StoresModelAndReturnsEntry(t *testing.T) {
	store := NewSessionStore()
	h := NewSessionsHandler(store, nil)
	res, ferr := h.handlePatch(t.Context(), HandlerCtx{}, []byte(`{"key":"agent:main:main","model":"openai/gpt-4o-mini"}`))
	if ferr != nil {
		t.Fatal(ferr)
	}
	m := res.(map[string]any)
	if m["ok"] != true || m["key"] != "agent:main:main" {
		t.Errorf("response shape: %+v", m)
	}
	entry := m["entry"].(map[string]any)
	if entry["model"] != "openai/gpt-4o-mini" {
		t.Errorf("entry.model = %v", entry["model"])
	}
	// Round-trip via the store.
	if got := store.Model("agent:main:main"); got != "openai/gpt-4o-mini" {
		t.Errorf("store.Model = %q", got)
	}
}

func TestSessionsPatch_RejectsMissingKey(t *testing.T) {
	h := NewSessionsHandler(NewSessionStore(), nil)
	_, ferr := h.handlePatch(t.Context(), HandlerCtx{}, []byte(`{"model":"openai/x"}`))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Errorf("want BAD_REQUEST for missing key, got %+v", ferr)
	}
}

// --- sessions.list handler -----------------------------------------------

func TestSessionsList_IncludesPatchedAndChatOnlySessions(t *testing.T) {
	store := NewSessionStore()
	store.Patch("agent:main:main", map[string]json.RawMessage{"model": json.RawMessage(`"openai/x"`)})
	chats := NewChatStore()
	chats.Append("agent:coding:main", "user", "hi") // chat-only session
	h := NewSessionsHandler(store, chats)

	res, ferr := h.handleList(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	m := res.(map[string]any)
	rows := m["sessions"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (patched + chat-only)", len(rows))
	}
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row["key"].(string)] = true
	}
	for _, want := range []string{"agent:main:main", "agent:coding:main"} {
		if !keys[want] {
			t.Errorf("missing %q in sessions: %+v", want, rows)
		}
	}
	// Envelope shape.
	for _, k := range []string{"ts", "path", "count", "defaults", "sessions"} {
		if _, ok := m[k]; !ok {
			t.Errorf("envelope missing %q", k)
		}
	}
}

// --- sessions.subscribe handler ------------------------------------------

func TestSessionsSubscribe_NoOpReturnsOK(t *testing.T) {
	h := NewSessionsHandler(NewSessionStore(), nil)
	res, ferr := h.handleSubscribe(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if res.(map[string]any)["ok"] != true {
		t.Errorf("subscribe should return {ok:true}: %+v", res)
	}
}

// --- chat.send override --------------------------------------------------

func TestChatHandler_PerSessionModelOverrideWinsOverAgentDefault(t *testing.T) {
	// Agent default says openai/gpt-4o-mini; UI patched the session to
	// deepseek/deepseek-reasoner. chat.send must pick the override and
	// route to the deepseek factory branch.
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{{{Kind: provider.DeltaText, Text: "ok"}}},
	}
	factory := &recordingFactory{provider: scripted}
	store := NewSessionStore()
	store.Patch("agent:main:main", map[string]json.RawMessage{
		"model": json.RawMessage(`"deepseek/deepseek-reasoner"`),
	})

	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		factory,
		NewChatStore(),
	).WithSessions(store)

	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"r-override"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	if factory.lastProviderName != "deepseek" {
		t.Errorf("factory got providerName %q, want deepseek (override)", factory.lastProviderName)
	}
	calls := scripted.requests()
	if len(calls) != 1 || string(calls[0].Model) != "deepseek/deepseek-reasoner" {
		t.Errorf("provider got model %q, want deepseek/deepseek-reasoner", calls[0].Model)
	}
}

func TestChatHandler_NoOverrideUsesAgentDefault(t *testing.T) {
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{{{Kind: provider.DeltaText, Text: "ok"}}},
	}
	factory := &recordingFactory{provider: scripted}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		factory,
		NewChatStore(),
	).WithSessions(NewSessionStore())

	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"r-default"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	if factory.lastProviderName != "openai" {
		t.Errorf("no override → factory should see openai, got %q", factory.lastProviderName)
	}
}

// recordingFactory captures the providerName each For() call requested.
type recordingFactory struct {
	provider         provider.Provider
	lastProviderName string
}

func (f *recordingFactory) For(providerName, agentID string) (provider.Provider, error) {
	f.lastProviderName = providerName
	return f.provider, nil
}

// --- compat -------------------------------------------------------------

func TestCompat_SessionsPatchResultShape(t *testing.T) {
	h := NewSessionsHandler(NewSessionStore(), nil)
	res, _ := h.handlePatch(context.Background(), HandlerCtx{}, []byte(`{"key":"k","model":"openai/x"}`))
	raw, _ := json.Marshal(res)
	for _, want := range []string{`"ok":true`, `"key":"k"`, `"entry":{`, `"model":"openai/x"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response missing expected fragment %q: %s", want, raw)
		}
	}
}
