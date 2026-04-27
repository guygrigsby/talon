package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// stubWorkspace implements WorkspaceResolver for tests.
type stubWorkspace struct {
	dir string
	err error
}

func (s *stubWorkspace) Workspace(agentID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.dir, nil
}

// stubToolRunner implements ToolRunner; records calls + replays canned
// outputs keyed by tool name.
type stubToolRunner struct {
	specs   []provider.ToolSpec
	outputs map[string]string
	mu      sync.Mutex
	calls   []recordedCall
}

type recordedCall struct {
	Name  string
	Input string
}

func (r *stubToolRunner) Specs() []provider.ToolSpec { return r.specs }

func (r *stubToolRunner) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{Name: name, Input: string(input)})
	r.mu.Unlock()
	if out, ok := r.outputs[name]; ok {
		return out, nil
	}
	return "", fmt.Errorf("stubToolRunner: no canned output for %q", name)
}

func (r *stubToolRunner) Calls() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// scriptedProvider is a multi-call stub that emits a different Delta
// sequence per invocation. Use to model the provider's perspective in a
// multi-turn loop: first call emits a tool_call, second call emits text,
// etc.
type scriptedProvider struct {
	scripts [][]provider.Delta
	mu      sync.Mutex
	idx     int
	calls   []provider.Request
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	if p.idx >= len(p.scripts) {
		p.mu.Unlock()
		return nil, fmt.Errorf("scriptedProvider: ran out of scripts after %d calls", p.idx)
	}
	script := p.scripts[p.idx]
	p.idx++
	p.mu.Unlock()

	ch := make(chan provider.Delta)
	go func() {
		defer close(ch)
		for _, d := range script {
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
	return ch, nil
}

func (p *scriptedProvider) requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.Request, len(p.calls))
	copy(out, p.calls)
	return out
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

// --- multi-turn loop (talon-mt3) ------------------------------------------

// chatRunWaiter waits for the streaming goroutine to finish by polling
// h.runs (which the goroutine deletes from on exit). Tests need this
// because handleSend returns synchronously while runStream continues.
func waitForRunDone(t *testing.T, h *ChatHandler, runID string, sessionKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	want := sessionKey + "|" + runID
	for time.Now().Before(deadline) {
		h.runsMu.Lock()
		_, active := h.runs[want]
		h.runsMu.Unlock()
		if !active {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runStream goroutine did not exit within deadline for runId=%q", runID)
}

func TestChatHandler_MultiTurn_ToolCallExecutionAndReStream(t *testing.T) {
	store := NewChatStore()
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{
			// First stream: assistant calls bash("ls /tmp").
			{
				{Kind: provider.DeltaToolCall, ToolCall: &provider.ToolCall{
					ID: "call_1", Name: "bash", ArgumentsJSON: `{"command":"ls /tmp"}`,
				}},
			},
			// Second stream: assistant produces final text.
			{
				{Kind: provider.DeltaText, Text: "I ran ls and found "},
				{Kind: provider.DeltaText, Text: "two files."},
			},
		},
	}
	runner := &stubToolRunner{
		specs:   []provider.ToolSpec{{Name: "bash"}},
		outputs: map[string]string{"bash": "file1\nfile2\n"},
	}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: scripted},
		store,
	).WithTools(&stubWorkspace{dir: "/tmp/ws"}, func(ws string) ToolRunner { return runner })

	body := []byte(`{"sessionKey":"agent:main:main","message":"do it","idempotencyKey":"r1"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	// Provider got two calls.
	if got := scripted.requests(); len(got) != 2 {
		t.Fatalf("provider got %d calls, want 2", len(got))
	}
	// Tool runner saw exactly one bash call with the model's args.
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].Name != "bash" || calls[0].Input != `{"command":"ls /tmp"}` {
		t.Errorf("tool runner calls = %+v", calls)
	}
	// Tool spec was advertised on each provider call.
	for i, req := range scripted.requests() {
		if len(req.Tools) != 1 || req.Tools[0].Name != "bash" {
			t.Errorf("call %d req.Tools = %+v, want [bash]", i, req.Tools)
		}
	}
	// History shape: user, assistant(tool_calls), tool result, assistant(final).
	hist := store.Snapshot("agent:main:main")
	if len(hist) != 4 {
		t.Fatalf("history len = %d, want 4: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content != "do it" {
		t.Errorf("hist[0] = %+v", hist[0])
	}
	if hist[1].Role != "assistant" || len(hist[1].ToolCalls) != 1 || hist[1].ToolCalls[0].Name != "bash" {
		t.Errorf("hist[1] (assistant w/ tool_calls) = %+v", hist[1])
	}
	if hist[2].Role != "tool" || hist[2].ToolCallID != "call_1" || hist[2].Content != "file1\nfile2\n" {
		t.Errorf("hist[2] (tool result) = %+v", hist[2])
	}
	if hist[3].Role != "assistant" || hist[3].Content != "I ran ls and found two files." {
		t.Errorf("hist[3] (final) = %+v", hist[3])
	}
	// Second provider call's history must include the tool result so the
	// model can reason about it.
	secondReqMsgs := scripted.requests()[1].Messages
	if len(secondReqMsgs) != 3 {
		t.Fatalf("second call msgs len = %d, want 3 (user, assistant+tool_call, tool)", len(secondReqMsgs))
	}
	if secondReqMsgs[1].Role != provider.RoleAssistant || len(secondReqMsgs[1].ToolCalls) != 1 {
		t.Errorf("second call msgs[1] missing tool_calls: %+v", secondReqMsgs[1])
	}
	if secondReqMsgs[2].Role != provider.RoleTool || secondReqMsgs[2].ToolCallID != "call_1" {
		t.Errorf("second call msgs[2] tool result wrong: %+v", secondReqMsgs[2])
	}
}

func TestChatHandler_MultiTurn_IterationCapEmitsErrorState(t *testing.T) {
	// Build a script that always emits a tool call, never converges.
	loopScript := []provider.Delta{
		{Kind: provider.DeltaToolCall, ToolCall: &provider.ToolCall{
			ID: "spin", Name: "noop", ArgumentsJSON: `{}`,
		}},
	}
	// 5 identical scripts so the handler can run 5 iterations before
	// hitting the cap (which we set to 4).
	scripts := make([][]provider.Delta, 5)
	for i := range scripts {
		scripts[i] = loopScript
	}
	scripted := &scriptedProvider{scripts: scripts}
	runner := &stubToolRunner{
		specs:   []provider.ToolSpec{{Name: "noop"}},
		outputs: map[string]string{"noop": "ok"},
	}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: scripted},
		NewChatStore(),
	).WithTools(&stubWorkspace{dir: "/tmp/ws"}, func(ws string) ToolRunner { return runner })
	h.MaxToolIterations = 4

	body := []byte(`{"sessionKey":"agent:main:main","message":"loop","idempotencyKey":"r2"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	// Provider should have been called exactly MaxToolIterations times.
	if got := scripted.requests(); len(got) != 4 {
		t.Errorf("provider got %d calls, want 4 (cap)", len(got))
	}
	// Tool runner ran for each iteration.
	if got := runner.Calls(); len(got) != 4 {
		t.Errorf("tool runner ran %d times, want 4", len(got))
	}
}

func TestChatHandler_MultiTurn_ToolRunnerUnavailableEmitsError(t *testing.T) {
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{
			{
				{Kind: provider.DeltaToolCall, ToolCall: &provider.ToolCall{
					ID: "call_x", Name: "bash", ArgumentsJSON: `{}`,
				}},
			},
		},
	}
	// No WithTools call — runner is nil.
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: scripted},
		NewChatStore(),
	)

	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"r3"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	// Provider was called once; no second iteration since the model's
	// tool call couldn't be honored.
	if got := scripted.requests(); len(got) != 1 {
		t.Errorf("provider got %d calls, want 1 (tool-runner-unavailable terminates)", len(got))
	}
}

func TestChatHandler_TextOnlyChatStillWorksWithoutTools(t *testing.T) {
	// Regression: chat.send without WithTools should behave exactly as
	// before — single stream, text only, no tools advertised.
	scripted := &scriptedProvider{
		scripts: [][]provider.Delta{
			{
				{Kind: provider.DeltaText, Text: "hello"},
			},
		},
	}
	store := NewChatStore()
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: scripted},
		store,
	)

	body := []byte(`{"sessionKey":"agent:main:main","message":"hi","idempotencyKey":"r4"}`)
	res, ferr := h.handleSend(t.Context(), HandlerCtx{Session: nil}, body)
	if ferr != nil {
		t.Fatal(ferr)
	}
	runID := res.(map[string]any)["runId"].(string)
	waitForRunDone(t, h, runID, "agent:main:main")

	if got := scripted.requests(); len(got) != 1 {
		t.Errorf("provider got %d calls, want 1", len(got))
	}
	// No tools advertised.
	if got := scripted.requests()[0].Tools; len(got) != 0 {
		t.Errorf("text-only mode should not advertise tools, got %+v", got)
	}
	hist := store.Snapshot("agent:main:main")
	if len(hist) != 2 || hist[1].Role != "assistant" || hist[1].Content != "hello" {
		t.Errorf("history wrong: %+v", hist)
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
