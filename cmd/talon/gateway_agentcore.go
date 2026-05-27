// buildAgentcoreRunner returns the AgentcoreRunFn the server's
// ChatHandler invokes for providers routed through agentcore.
// Lives in cmd/talon (not internal/server) so internal/server
// doesn't take a transitive dep on agentcore + LiteLLM. The fn
// captures the gateway's Paths so each invocation reads fresh
// merged config.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/agentcore_chat"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildAgentcoreRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.AgentcoreRunFn {
	return func(
		ctx context.Context,
		agentID, sessionKey, runID, userText, selectedModelID string,
		priorHistory []server.ChatMessage,
		emitText func(seq int, state, full, delta string),
		emitToolStart func(toolCallID, name, args string),
		emitToolResult func(toolCallID, name, output string, isErr bool),
		emitError func(seq int, kind, msg string),
	) (server.AgentcoreRunResult, error) {
		sink := &gatewayEventSink{
			text:       emitText,
			toolStart:  emitToolStart,
			toolResult: emitToolResult,
			err:        emitError,
		}

		merged, err := config.MergedBytes(paths)
		if err != nil {
			sink.Error("merged-config", err.Error())
			return server.AgentcoreRunResult{}, fmt.Errorf("read merged config: %w", err)
		}

		builder := agentcore_chat.NewBuilder(merged, paths)
		if selectedModelID != "" {
			builder = builder.WithSelectedModel(selectedModelID)
		}
		if mem != nil {
			builder = builder.
				WithMemory(mem.Store, mem.Recaller).
				WithMemoryOptions(mem.MaxRecallEntries, mem.MemoryHeader, mem.Kinds).
				WithMemorySource(sessionKey, runID)
		}
		agent, choice, err := builder.BuildAgent(agentID)
		if err != nil {
			sink.Error("build-agent", err.Error())
			return server.AgentcoreRunResult{}, err
		}

		adapter := agentcore_chat.NewEventAdapter(sink)
		unsub := agent.Subscribe(func(ev agentcore.Event) { adapter.Handle(ev) })
		defer unsub()

		if history := agentcoreHistoryFromChatStore(priorHistory); len(history) > 0 {
			if err := agent.SetMessages(history); err != nil {
				sink.Error("seed-history", err.Error())
				return server.AgentcoreRunResult{}, fmt.Errorf("seed history: %w", err)
			}
		}

		if err := agent.Prompt(userText); err != nil {
			sink.Error("prompt", err.Error())
			return server.AgentcoreRunResult{}, err
		}

		done := make(chan struct{})
		go func() {
			agent.WaitForIdle()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			agent.Abort()
			<-done
			return server.AgentcoreRunResult{}, ctx.Err()
		}

		final, _ := adapter.Snapshot()
		usage := agent.TotalUsage()
		return server.AgentcoreRunResult{
			FinalText: final,
			ModelID:   choice.ID,
			Usage: server.AgentcoreUsage{
				InputTokens:  usage.Input,
				OutputTokens: usage.Output,
			},
		}, nil
	}
}

func agentcoreHistoryFromChatStore(history []server.ChatMessage) []agentcore.AgentMessage {
	out := make([]agentcore.AgentMessage, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, agentcore.Message{
				Role:      agentcore.RoleUser,
				Content:   []agentcore.ContentBlock{agentcore.TextBlock(m.Content)},
				Timestamp: m.At,
			})
		case "assistant":
			blocks := make([]agentcore.ContentBlock, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, agentcore.TextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				args := strings.TrimSpace(tc.ArgumentsJSON)
				if args == "" {
					args = "{}"
				}
				blocks = append(blocks, agentcore.ToolCallBlock(agentcore.ToolCall{
					ID:   tc.ID,
					Name: tc.Name,
					Args: json.RawMessage(args),
				}))
			}
			out = append(out, agentcore.Message{
				Role:      agentcore.RoleAssistant,
				Content:   blocks,
				Timestamp: m.At,
			})
		case "tool":
			msg := agentcore.ToolResultMsg(m.ToolCallID, json.RawMessage(m.Content), false)
			msg.Timestamp = m.At
			out = append(out, msg)
		case "system":
			out = append(out, agentcore.Message{
				Role:      agentcore.RoleSystem,
				Content:   []agentcore.ContentBlock{agentcore.TextBlock(m.Content)},
				Timestamp: m.At,
			})
		}
	}
	return out
}

// gatewayEventSink adapts agentcore_chat.EventSink onto the four
// emit closures the server-side AgentcoreRunFn contract exposes.
// One per chat.send goroutine; no concurrent calls expected, but
// the underlying emit functions are themselves serialized by the
// server's seq counter.
type gatewayEventSink struct {
	text       func(seq int, state, full, delta string)
	toolStart  func(id, name, args string)
	toolResult func(id, name, output string, isErr bool)
	err        func(seq int, kind, msg string)
}

func (s *gatewayEventSink) Delta(full, delta string) {
	s.text(0, "delta", full, delta) // seq managed by wrapper in server
}
func (s *gatewayEventSink) Thinking(full, delta string) {
	s.text(0, "thinking", full, delta)
}
func (s *gatewayEventSink) Final(full string) {
	s.text(0, "final", full, "")
}
func (s *gatewayEventSink) ToolStart(id, name, args string) {
	s.toolStart(id, name, args)
}
func (s *gatewayEventSink) ToolResult(id, name, output string, isErr bool) {
	s.toolResult(id, name, output, isErr)
}
func (s *gatewayEventSink) Error(kind, msg string) {
	s.err(0, kind, msg)
}
