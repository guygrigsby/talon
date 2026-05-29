package main

import (
	"testing"
	"time"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
)

func TestChatHistoryFromChatStore(t *testing.T) {
	at := time.Unix(123, 0)
	got := chatHistoryFromChatStore([]server.ChatMessage{
		{Role: "user", Content: "remember blue", At: at},
		{
			Role:    "assistant",
			Content: "checking",
			ToolCalls: []provider.ToolCall{{
				ID:            "call_1",
				Name:          "bash",
				ArgumentsJSON: `{"cmd":"pwd"}`,
			}},
			At: at,
		},
		{Role: "tool", Content: "tool output", ToolCallID: "call_1", At: at},
	})

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	user := mustAgentMessage(t, got[0])
	if user.Role != agentcore.RoleUser || user.TextContent() != "remember blue" {
		t.Fatalf("user message = %+v", user)
	}
	assistant := mustAgentMessage(t, got[1])
	if assistant.Role != agentcore.RoleAssistant || assistant.TextContent() != "checking" {
		t.Fatalf("assistant message = %+v", assistant)
	}
	calls := assistant.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "bash" || string(calls[0].Args) != `{"cmd":"pwd"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
	tool := mustAgentMessage(t, got[2])
	if tool.Role != agentcore.RoleTool || tool.TextContent() != "tool output" {
		t.Fatalf("tool message = %+v", tool)
	}
	if tool.Metadata["tool_call_id"] != "call_1" {
		t.Fatalf("tool metadata = %+v", tool.Metadata)
	}
}

func mustAgentMessage(t *testing.T, msg agentcore.AgentMessage) agentcore.Message {
	t.Helper()
	m, ok := msg.(agentcore.Message)
	if !ok {
		t.Fatalf("message type = %T, want agentcore.Message", msg)
	}
	return m
}
