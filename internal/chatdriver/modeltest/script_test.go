package modeltest

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// drain collects every chunk a Stream emits, in order.
func drain(t *testing.T, m model.Model, msgs []message.Message) []model.Chunk {
	t.Helper()
	ch, err := m.Stream(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out []model.Chunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestModel_TextOnly_StreamsDeltasThenDone(t *testing.T) {
	m := New(Turn{Text: []string{"Hello, ", "world"}, StopReason: "stop"})

	chunks := drain(t, m, []message.Message{message.UserText("hi")})

	// Two text deltas, then a Done chunk.
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks (2 deltas + done), got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Delta != "Hello, " || chunks[0].DeltaKind != event.DeltaText {
		t.Errorf("chunk0 = %q/%q, want text delta", chunks[0].Delta, chunks[0].DeltaKind)
	}
	if chunks[1].Delta != "world" {
		t.Errorf("chunk1 delta = %q", chunks[1].Delta)
	}
	done := chunks[2]
	if !done.Done {
		t.Fatal("last chunk should be Done")
	}
	if done.Message.Text() != "Hello, world" {
		t.Errorf("done message text = %q, want %q", done.Message.Text(), "Hello, world")
	}
	if done.StopReason != "stop" {
		t.Errorf("stop reason = %q", done.StopReason)
	}
}

func TestModel_ReasoningThenText(t *testing.T) {
	m := New(Turn{Reasoning: []string{"let me think"}, Text: []string{"answer"}})

	chunks := drain(t, m, nil)

	if chunks[0].DeltaKind != event.DeltaThinking || chunks[0].Delta != "let me think" {
		t.Errorf("chunk0 = %q/%q, want thinking delta", chunks[0].Delta, chunks[0].DeltaKind)
	}
	if chunks[1].DeltaKind != event.DeltaText || chunks[1].Delta != "answer" {
		t.Errorf("chunk1 = %q/%q, want text delta", chunks[1].Delta, chunks[1].DeltaKind)
	}
	if !chunks[len(chunks)-1].Done {
		t.Error("stream must end Done")
	}
}

func TestModel_ToolRound_TwoStreamCalls(t *testing.T) {
	m := New(
		Turn{ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Args: `{"cmd":"ls"}`}}, StopReason: "tool_use"},
		Turn{Text: []string{"done"}, StopReason: "stop"},
	)

	// First call: the model asks for a tool.
	first := drain(t, m, []message.Message{message.UserText("run ls")})
	done1 := first[len(first)-1]
	if !done1.Done {
		t.Fatal("first turn must end Done")
	}
	var calls []message.ContentBlock
	for _, b := range done1.Message.Content {
		if b.Kind == message.BlockToolCall {
			calls = append(calls, b)
		}
	}
	if len(calls) != 1 || calls[0].ToolName != "bash" || calls[0].ToolID != "c1" {
		t.Fatalf("expected one bash tool call, got %+v", done1.Message.Content)
	}
	if string(calls[0].Args) != `{"cmd":"ls"}` {
		t.Errorf("tool args = %s", calls[0].Args)
	}

	// Second call (loop calls back after running the tool): the answer.
	second := drain(t, m, []message.Message{message.UserText("run ls")})
	if got := second[len(second)-1].Message.Text(); got != "done" {
		t.Errorf("second turn text = %q, want %q", got, "done")
	}
}

func TestModel_CapturesMessagesAndTools(t *testing.T) {
	m := New(Turn{Text: []string{"ok"}})
	tools := []model.ToolSpec{{Name: "bash", Description: "run a command"}}
	ch, err := m.Stream(context.Background(), []message.Message{message.UserText("hello")}, tools)
	if err != nil {
		t.Fatal(err)
	}
	for range ch { //nolint:revive // drain
	}
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 captured call, got %d", len(calls))
	}
	if len(calls[0].Messages) != 1 || calls[0].Messages[0].Text() != "hello" {
		t.Errorf("captured messages = %+v", calls[0].Messages)
	}
	if len(calls[0].Tools) != 1 || calls[0].Tools[0].Name != "bash" {
		t.Errorf("captured tools = %+v", calls[0].Tools)
	}
}

func TestModel_OverrunReturnsError(t *testing.T) {
	m := New(Turn{Text: []string{"only one turn"}})
	_ = drain(t, m, nil)       // consume the one turn
	chunks := drain(t, m, nil) // one call too many
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Error("calling Stream more times than scripted turns should yield an Err chunk")
	}
}

func TestModel_SupportsToolsDefaultsTrue(t *testing.T) {
	if !New().SupportsTools() {
		t.Error("default model should support tools")
	}
	if New().WithToolsUnsupported().SupportsTools() {
		t.Error("WithToolsUnsupported should disable tool support")
	}
}
