package connectapi

import (
	"testing"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// Translation tests for the (event-name, payload) → typed
// ChatEvent oneof mapping. Pure go — no HTTP, no server boot.
// Live end-to-end (chat handler → SinkRegistry → Connect stream)
// is covered by the integration test in server_test.go on the
// server package.

func TestTranslateEvent_ChatDelta(t *testing.T) {
	payload := server.ChatEventPayload{
		RunID: "r1", SessionKey: "k", Seq: 3, State: "delta", DeltaText: " world",
		Message: &server.ChatEventMessage{
			Content: []server.ChatEventContentPart{{Type: "text", Text: "hello world"}},
		},
	}
	got := translateEvent("chat", payload)
	if got == nil {
		t.Fatal("delta should translate")
	}
	if got.GetRunId() != "r1" || got.GetSessionKey() != "k" || got.GetSeq() != 3 {
		t.Errorf("meta wrong: run=%q sess=%q seq=%d", got.GetRunId(), got.GetSessionKey(), got.GetSeq())
	}
	d := got.GetDelta()
	if d == nil {
		t.Fatalf("expected Delta variant, got %T", got.GetPayload())
	}
	if d.GetCumulative() != "hello world" || d.GetDeltaText() != " world" {
		t.Errorf("delta fields wrong: %+v", d)
	}
}

func TestTranslateEvent_ChatFinal(t *testing.T) {
	payload := server.ChatEventPayload{
		RunID: "r", SessionKey: "k", Seq: 9, State: "final", StopReason: "end_turn",
		Message: &server.ChatEventMessage{
			Content: []server.ChatEventContentPart{{Type: "text", Text: "done"}},
		},
	}
	got := translateEvent("chat", payload)
	f := got.GetFinal()
	if f == nil {
		t.Fatalf("expected Final variant, got %T", got.GetPayload())
	}
	if f.GetText() != "done" || f.GetStopReason() != "end_turn" {
		t.Errorf("final fields wrong: %+v", f)
	}
}

func TestTranslateEvent_ChatError(t *testing.T) {
	got := translateEvent("chat", server.ChatEventPayload{
		State: "error", ErrorKind: "provider", ErrorMessage: "rate limited",
	})
	e := got.GetError()
	if e == nil {
		t.Fatalf("expected Error variant, got %T", got.GetPayload())
	}
	if e.GetKind() != "provider" || e.GetMessage() != "rate limited" {
		t.Errorf("error fields wrong: %+v", e)
	}
}

func TestTranslateEvent_ChatAborted(t *testing.T) {
	got := translateEvent("chat", server.ChatEventPayload{
		State: "aborted",
		Message: &server.ChatEventMessage{
			Content: []server.ChatEventContentPart{{Type: "text", Text: "partial"}},
		},
	})
	a := got.GetAborted()
	if a == nil {
		t.Fatalf("expected Aborted variant, got %T", got.GetPayload())
	}
	if a.GetText() != "partial" {
		t.Errorf("aborted text = %q", a.GetText())
	}
}

func TestTranslateEvent_ChatUnknownStateDrops(t *testing.T) {
	got := translateEvent("chat", server.ChatEventPayload{State: "thinking"})
	if got != nil {
		t.Errorf("unknown state should drop; got %+v", got)
	}
}

func TestTranslateEvent_AgentToolStart(t *testing.T) {
	payload := server.AgentEventPayload{
		Stream: "tool", SessionKey: "k", RunID: "r", Ts: 1700000000000,
		Data: map[string]any{
			"toolCallId": "call_a",
			"name":       "glob",
			"phase":      "start",
			"args":       map[string]any{"pattern": "*.go"},
		},
	}
	got := translateEvent("agent", payload)
	ts := got.GetToolStart()
	if ts == nil {
		t.Fatalf("expected ToolStart variant, got %T", got.GetPayload())
	}
	if ts.GetToolCallId() != "call_a" || ts.GetName() != "glob" {
		t.Errorf("tool start fields wrong: %+v", ts)
	}
	if ts.GetArgsJson() != `{"pattern":"*.go"}` {
		t.Errorf("args_json round-trip wrong: %q", ts.GetArgsJson())
	}
}

func TestTranslateEvent_AgentToolResult(t *testing.T) {
	got := translateEvent("agent", server.AgentEventPayload{
		Stream: "tool", SessionKey: "k", RunID: "r",
		Data: map[string]any{
			"toolCallId": "call_a",
			"name":       "bash",
			"phase":      "result",
			"result":     "exit 0",
		},
	})
	tr := got.GetToolResult()
	if tr == nil {
		t.Fatalf("expected ToolResult variant, got %T", got.GetPayload())
	}
	if tr.GetToolCallId() != "call_a" || tr.GetName() != "bash" || tr.GetOutput() != "exit 0" {
		t.Errorf("tool result fields wrong: %+v", tr)
	}
}

// Unknown phases drop. The WS path only emits start + result today;
// any future phase would need a proto variant before it can flow.
func TestTranslateEvent_AgentUnknownPhaseDrops(t *testing.T) {
	got := translateEvent("agent", server.AgentEventPayload{
		Stream: "tool",
		Data:   map[string]any{"phase": "midway"},
	})
	if got != nil {
		t.Errorf("unknown phase should drop; got %+v", got)
	}
}

func TestTranslateEvent_UnknownEventNameDrops(t *testing.T) {
	if got := translateEvent("channels.created", map[string]any{"x": 1}); got != nil {
		t.Errorf("unknown event name should drop; got %+v", got)
	}
}

// Type assertions are defensive: a future emitter that uses a
// different payload shape under the same event name shouldn't
// panic the stream.
func TestTranslateEvent_WrongPayloadTypeDrops(t *testing.T) {
	if got := translateEvent("chat", "not a payload"); got != nil {
		t.Errorf("wrong payload type should drop; got %+v", got)
	}
	if got := translateEvent("agent", 42); got != nil {
		t.Errorf("wrong payload type should drop; got %+v", got)
	}
}

func TestStringifyArgs(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string-passthrough", "raw arg blob", "raw arg blob"},
		{"map", map[string]any{"k": "v"}, `{"k":"v"}`},
		{"slice", []any{1, 2}, `[1,2]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stringifyArgs(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Compile-time guard that connectSink still satisfies the
// EventSink interface — if a future refactor renames the method
// or changes the signature, this fails at build time rather than
// at first stream open.
var _ server.EventSink = (*connectSink)(nil)

// Compile-time guard for the typed message construction so a
// proto regen that drops one of the variants fails the build.
func TestChatEvent_OneofWrapperTypesExist(t *testing.T) {
	_ = &talonv1.ChatEvent_Delta{Delta: &talonv1.ChatDelta{}}
	_ = &talonv1.ChatEvent_Final{Final: &talonv1.ChatFinal{}}
	_ = &talonv1.ChatEvent_Aborted{Aborted: &talonv1.ChatAborted{}}
	_ = &talonv1.ChatEvent_Error{Error: &talonv1.ChatError{}}
	_ = &talonv1.ChatEvent_ToolStart{ToolStart: &talonv1.ToolStart{}}
	_ = &talonv1.ChatEvent_ToolResult{ToolResult: &talonv1.ToolResult{}}
}
