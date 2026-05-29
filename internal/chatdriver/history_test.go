package chatdriver

import (
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
)

func TestChatMessagesToJess_AllRoles(t *testing.T) {
	in := []server.ChatMessage{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "checking", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "recall", ArgumentsJSON: `{"q":"x"}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "recall", Content: `{"hits":0}`},
		{Role: "assistant", Content: "no hits"},
	}
	got := ChatMessagesToJess(in)
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	if got[0].Role != message.RoleSystem || got[0].Text() != "you are helpful" {
		t.Errorf("msg[0] = %+v", got[0])
	}
	if got[1].Role != message.RoleUser || got[1].Text() != "hi" {
		t.Errorf("msg[1] = %+v", got[1])
	}
	// Assistant with tool call: one text block + one tool-call block.
	if got[2].Role != message.RoleAssistant || len(got[2].Content) != 2 {
		t.Fatalf("msg[2] = %+v", got[2])
	}
	if got[2].Content[1].Kind != message.BlockToolCall || got[2].Content[1].ToolID != "c1" || got[2].Content[1].ToolName != "recall" {
		t.Errorf("tool-call block = %+v", got[2].Content[1])
	}
	// Tool result.
	if got[3].Role != message.RoleTool || len(got[3].Content) != 1 || got[3].Content[0].Kind != message.BlockToolResult {
		t.Fatalf("msg[3] = %+v", got[3])
	}
	if got[3].Content[0].ToolID != "c1" || string(got[3].Content[0].Result) != `{"hits":0}` {
		t.Errorf("tool-result = %+v", got[3].Content[0])
	}
	if got[4].Role != message.RoleAssistant || got[4].Text() != "no hits" {
		t.Errorf("msg[4] = %+v", got[4])
	}
	// Args round-trips as JSON.
	if !json.Valid(got[2].Content[1].Args) {
		t.Errorf("tool-call args not valid JSON: %s", got[2].Content[1].Args)
	}
}

func TestChatMessagesToJess_EmptyToolCallArgs(t *testing.T) {
	// Empty/whitespace args must become "{}" so the model sees valid JSON.
	in := []server.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "recall", ArgumentsJSON: "   "}}},
	}
	got := ChatMessagesToJess(in)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("got = %+v", got)
	}
	if string(got[0].Content[0].Args) != "{}" {
		t.Errorf("args = %q, want %q", got[0].Content[0].Args, "{}")
	}
}
