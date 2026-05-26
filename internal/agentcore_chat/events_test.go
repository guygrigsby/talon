package agentcore_chat

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/voocel/agentcore"
)

// recordingSink captures every sink call as a typed event. Tests
// compare against this rather than the live wire frames.
type sinkCall struct {
	Kind    string // "delta" | "thinking" | "final" | "tool_start" | "tool_result" | "error"
	Full    string
	Delta   string
	ID      string
	Name    string
	Args    string
	Output  string
	IsError bool
	ErrMsg  string
	ErrKind string
}

type recordingSink struct {
	calls []sinkCall
}

func (r *recordingSink) Delta(full, delta string) {
	r.calls = append(r.calls, sinkCall{Kind: "delta", Full: full, Delta: delta})
}
func (r *recordingSink) Thinking(full, delta string) {
	r.calls = append(r.calls, sinkCall{Kind: "thinking", Full: full, Delta: delta})
}
func (r *recordingSink) Final(full string) {
	r.calls = append(r.calls, sinkCall{Kind: "final", Full: full})
}
func (r *recordingSink) ToolStart(id, name, args string) {
	r.calls = append(r.calls, sinkCall{Kind: "tool_start", ID: id, Name: name, Args: args})
}
func (r *recordingSink) ToolResult(id, name, output string, isError bool) {
	r.calls = append(r.calls, sinkCall{Kind: "tool_result", ID: id, Name: name, Output: output, IsError: isError})
}
func (r *recordingSink) Error(kind, msg string) {
	r.calls = append(r.calls, sinkCall{Kind: "error", ErrKind: kind, ErrMsg: msg})
}

func TestEventAdapter_TextDeltasAccumulate(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventMessageStart, Message: stubAssistantMessage("")})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "Hello"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: ", world"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "!"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageEnd, Message: stubAssistantMessage("Hello, world!")})

	want := []sinkCall{
		{Kind: "delta", Full: "Hello", Delta: "Hello"},
		{Kind: "delta", Full: "Hello, world", Delta: ", world"},
		{Kind: "delta", Full: "Hello, world!", Delta: "!"},
		{Kind: "final", Full: "Hello, world!"},
	}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Errorf("got %+v\nwant %+v", sink.calls, want)
	}
}

func TestEventAdapter_ThinkingTrackedSeparately(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventMessageStart, Message: stubAssistantMessage("")})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaThinking, Delta: "let me think"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaThinking, Delta: " about it"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "Result: 42"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageEnd, Message: stubAssistantMessage("Result: 42")})

	if len(sink.calls) != 4 {
		t.Fatalf("got %d calls, want 4: %+v", len(sink.calls), sink.calls)
	}
	if sink.calls[0].Kind != "thinking" || sink.calls[0].Full != "let me think" {
		t.Errorf("first call should be thinking 'let me think', got %+v", sink.calls[0])
	}
	if sink.calls[1].Full != "let me think about it" {
		t.Errorf("thinking accumulation wrong: %q", sink.calls[1].Full)
	}
	if sink.calls[2].Kind != "delta" || sink.calls[2].Full != "Result: 42" {
		t.Errorf("text delta should accumulate independently: %+v", sink.calls[2])
	}
	if sink.calls[3].Kind != "final" || sink.calls[3].Full != "Result: 42" {
		t.Errorf("final should reflect text only: %+v", sink.calls[3])
	}
}

func TestEventAdapter_ToolCallRoundtrip(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	args := json.RawMessage(`{"path":"foo.txt"}`)
	result := json.RawMessage(`"file contents here"`)
	a.Handle(agentcore.Event{Type: agentcore.EventToolExecStart, ToolID: "call-1", Tool: "read", Args: args})
	a.Handle(agentcore.Event{Type: agentcore.EventToolExecEnd, ToolID: "call-1", Tool: "read", Result: result})

	want := []sinkCall{
		{Kind: "tool_start", ID: "call-1", Name: "read", Args: string(args)},
		{Kind: "tool_result", ID: "call-1", Name: "read", Output: string(result)},
	}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Errorf("got %+v\nwant %+v", sink.calls, want)
	}
}

func TestEventAdapter_ToolErrorFlag(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventToolExecEnd, ToolID: "c", Tool: "bash", Result: json.RawMessage(`"oops"`), IsError: true})
	if got := sink.calls[0].IsError; !got {
		t.Errorf("isError flag should propagate")
	}
}

func TestEventAdapter_ErrorEvent(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventError, Err: errors.New("provider down")})
	if len(sink.calls) != 1 || sink.calls[0].Kind != "error" || sink.calls[0].ErrMsg != "provider down" || sink.calls[0].ErrKind != "agent" {
		t.Errorf("got %+v", sink.calls)
	}
}

func TestEventAdapter_EmptyDeltaSkipped(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: ""})
	if len(sink.calls) != 0 {
		t.Errorf("empty delta should not produce a sink call: %+v", sink.calls)
	}
}

func TestEventAdapter_FinalFallsBackToMessageText(t *testing.T) {
	// Some providers emit no streaming deltas — go straight from
	// message_start to message_end. The final should pull from the
	// message's TextContent in that case.
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventMessageStart, Message: stubAssistantMessage("")})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageEnd, Message: stubAssistantMessage("complete reply")})
	if len(sink.calls) != 1 || sink.calls[0].Kind != "final" || sink.calls[0].Full != "complete reply" {
		t.Errorf("got %+v", sink.calls)
	}
}

func TestEventAdapter_NewTurnResetsAccumulator(t *testing.T) {
	// Multi-turn: first turn's text shouldn't leak into the second
	// turn's deltas.
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(agentcore.Event{Type: agentcore.EventMessageStart, Message: stubAssistantMessage("")})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "turn1"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageEnd, Message: stubAssistantMessage("turn1")})

	a.Handle(agentcore.Event{Type: agentcore.EventMessageStart, Message: stubAssistantMessage("")})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "turn2"})
	a.Handle(agentcore.Event{Type: agentcore.EventMessageEnd, Message: stubAssistantMessage("turn2")})

	// Find the second turn's delta call.
	for _, c := range sink.calls[2:] {
		if c.Kind == "delta" {
			if c.Full != "turn2" {
				t.Errorf("second turn delta should reset accumulator; got Full=%q", c.Full)
			}
			return
		}
	}
	t.Error("second turn delta missing")
}

// stubAssistantMessage builds a minimal AgentMessage for tests.
// agentcore.Message satisfies the AgentMessage interface; we
// construct it directly because there's no public constructor that
// also takes raw text.
func stubAssistantMessage(text string) agentcore.AgentMessage {
	m := agentcore.Message{Role: agentcore.RoleAssistant}
	if text != "" {
		m.Content = []agentcore.ContentBlock{agentcore.TextBlock(text)}
	}
	return m
}
