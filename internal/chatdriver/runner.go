package chatdriver

import (
	"context"
	"fmt"

	"github.com/guygrigsby/jess/memory"
	jessmsg "github.com/guygrigsby/jess/message"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// NewChatRunner constructs the jess-backed per-turn ChatRunFn. The runner
// rebuilds the agent each turn, seeds the Session from ChatStore history,
// streams events to the EventSink, persists the final assistant text back
// through the server layer, and returns ChatRunResult with usage from jess.
func NewChatRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.ChatRunFn {
	return func(
		ctx context.Context,
		agentID, sessionKey, runID, userText, selectedModelID string,
		priorHistory []server.ChatMessage,
		emitText func(seq int, state, full, delta string),
		emitToolStart func(toolCallID, name, args string),
		emitToolResult func(toolCallID, name, output string, isErr bool),
		emitError func(seq int, kind, msg string),
	) (server.ChatRunResult, error) {
		sink := &gatewayEventSink{
			text:       emitText,
			toolStart:  emitToolStart,
			toolResult: emitToolResult,
			err:        emitError,
		}
		adapter := NewEventAdapter(sink)

		merged, err := config.MergedBytes(paths)
		if err != nil {
			return server.ChatRunResult{}, fmt.Errorf("merged config: %w", err)
		}
		builder := NewBuilder(merged, paths)
		if selectedModelID != "" {
			builder = builder.WithSelectedModel(selectedModelID)
		}
		if mem != nil {
			builder = builder.
				WithMemory(mem.Store, mem.Recaller).
				WithMemorySource(sessionKey, runID)
		}
		agent, choice, err := builder.BuildAgent(agentID)
		if err != nil {
			return server.ChatRunResult{}, err
		}

		sess, err := agent.NewSessionWithHistory(ChatMessagesToJess(priorHistory))
		if err != nil {
			return server.ChatRunResult{}, err
		}

		promptCtx := memory.WithSource(ctx, memory.Source{
			SessionID: sessionKey,
			MessageID: runID,
			Tool:      "remember",
			Reason:    "model decided",
		})

		run, err := sess.Prompt(promptCtx, userText)
		if err != nil {
			return server.ChatRunResult{}, err
		}

		for ev := range run.Events() {
			adapter.Handle(ev)
		}
		res, runErr := run.Wait()
		final := lastAssistantText(res.Messages)
		adapter.Finalize(final)

		usage := server.ChatUsage{}
		if res.Summary != nil {
			usage.InputTokens = res.Summary.Usage.Input
			usage.OutputTokens = res.Summary.Usage.Output
		}
		return server.ChatRunResult{
			FinalText: final,
			ModelID:   choice.ID,
			Usage:     usage,
		}, runErr
	}
}

func lastAssistantText(msgs []jessmsg.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == jessmsg.RoleAssistant {
			return msgs[i].Text()
		}
	}
	return ""
}

// gatewayEventSink implements EventSink by calling the runner's emit closures.
type gatewayEventSink struct {
	text       func(seq int, state, full, delta string)
	toolStart  func(toolCallID, name, args string)
	toolResult func(toolCallID, name, output string, isErr bool)
	err        func(seq int, kind, msg string)
}

func (s *gatewayEventSink) Delta(full, delta string)        { s.text(0, "delta", full, delta) }
func (s *gatewayEventSink) Thinking(full, delta string)     { s.text(0, "thinking", full, delta) }
func (s *gatewayEventSink) Final(full string)               { s.text(0, "final", full, "") }
func (s *gatewayEventSink) ToolStart(id, name, args string) { s.toolStart(id, name, args) }
func (s *gatewayEventSink) ToolResult(id, name, out string, isErr bool) {
	s.toolResult(id, name, out, isErr)
}
func (s *gatewayEventSink) Error(kind, msg string) { s.err(0, kind, msg) }
