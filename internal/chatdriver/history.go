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
			// ChatStore stores tool outputs as plain strings (some tools emit
			// JSON, e.g. recall; many emit plain text, e.g. ls -> "file1\nfile2\n").
			// jess expects Result to be valid JSON, so quote non-JSON outputs as
			// a JSON string. Loss-less: a downstream consumer reading the string
			// gets the original tool output back via json.Unmarshal.
			result := json.RawMessage(m.Content)
			if !json.Valid(result) {
				if quoted, err := json.Marshal(m.Content); err == nil {
					result = quoted
				}
			}
			out = append(out, message.Message{
				Role: message.RoleTool,
				Content: []message.ContentBlock{{
					Kind:    message.BlockToolResult,
					ToolID:  m.ToolCallID,
					Result:  result,
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
