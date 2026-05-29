package chatdriver

import (
	"encoding/json"
	"strings"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/talon/internal/server"
)

// ChatMessagesToJess converts talon's stored chat history into jess
// message.Messages suitable for seeding a Session via
// jess.Agent.NewSessionWithHistory. Mirrors the deleted
// agentcoreHistoryFromChatStore but produces jess types. Unknown roles
// are skipped (defensive; shouldn't occur in valid ChatStore state).
func ChatMessagesToJess(history []server.ChatMessage) []message.Message {
	out := make([]message.Message, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, message.Message{
				Role:    message.RoleUser,
				Content: []message.ContentBlock{{Kind: message.BlockText, Text: m.Content}},
			})
		case "assistant":
			blocks := make([]message.ContentBlock, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, message.ContentBlock{Kind: message.BlockText, Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := strings.TrimSpace(tc.ArgumentsJSON)
				if args == "" {
					args = "{}"
				}
				blocks = append(blocks, message.ContentBlock{
					Kind:     message.BlockToolCall,
					ToolID:   tc.ID,
					ToolName: tc.Name,
					Args:     json.RawMessage(args),
				})
			}
			out = append(out, message.Message{Role: message.RoleAssistant, Content: blocks})
		case "tool":
			out = append(out, message.Message{
				Role: message.RoleTool,
				Content: []message.ContentBlock{{
					Kind:    message.BlockToolResult,
					ToolID:  m.ToolCallID,
					Result:  json.RawMessage(m.Content),
					IsError: false,
				}},
			})
		case "system":
			out = append(out, message.Message{
				Role:    message.RoleSystem,
				Content: []message.ContentBlock{{Kind: message.BlockText, Text: m.Content}},
			})
		}
	}
	return out
}
