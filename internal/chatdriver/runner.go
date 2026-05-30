package chatdriver

import (
	"context"
	"fmt"

	"github.com/guygrigsby/jess/memory"
	jessmsg "github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// ClaudeMemoryResolver returns the ADR 0013 Claude-memory index +
// claude_memory tool for the current run. Returning ok=false leaves the
// feature off (no index injection, no tool registered). Resolved per-run
// so the index reflects the live MEMORY.md without restart. Nil is
// equivalent to a resolver that always returns ok=false.
type ClaudeMemoryResolver func() (index string, tl tool.Tool, ok bool)

// NewChatRunner constructs the jess-backed per-turn ChatRunFn. The runner
// rebuilds the agent each turn, seeds the Session from ChatStore history,
// streams events to the EventSink, persists the final assistant text back
// through the server layer, and returns ChatRunResult with usage from jess.
// claudeMem is optional; nil disables ADR 0013 Claude-memory access.
// RunnerOption tweaks the runner's per-turn agent build. Production passes
// none; tests use WithModelOverride to inject a deterministic model.
type RunnerOption func(*runnerConfig)

type runnerConfig struct {
	modelOverride model.Model
}

// WithModelOverride makes every turn build its agent against m instead of the
// config-driven LiteLLM model, skipping provider auth (ADR 0016 test seam).
func WithModelOverride(m model.Model) RunnerOption {
	return func(c *runnerConfig) { c.modelOverride = m }
}

func NewChatRunner(paths talonpath.Paths, mem *server.MemoryConfig, claudeMem ClaudeMemoryResolver, opts ...RunnerOption) server.ChatRunFn {
	var rc runnerConfig
	for _, o := range opts {
		o(&rc)
	}
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

		// fail emits a sink error before returning so the FE sees a failure
		// signal on the early-return paths (the server goroutine does not emit
		// errors for a returned err; without this the UI would see a silent
		// hang on config / build / session / prompt failures).
		fail := func(kind string, err error) (server.ChatRunResult, error) {
			emitError(0, kind, err.Error())
			return server.ChatRunResult{}, err
		}

		merged, err := config.MergedBytes(paths)
		if err != nil {
			return fail("config", fmt.Errorf("merged config: %w", err))
		}
		builder := NewBuilder(merged, paths)
		if rc.modelOverride != nil {
			builder = builder.WithModel(rc.modelOverride)
		}
		if selectedModelID != "" {
			builder = builder.WithSelectedModel(selectedModelID)
		}
		if mem != nil {
			builder = builder.WithMemory(mem.Store, mem.Recaller)
		}
		// ADR 0013: read-only Claude-memory access, gated by
		// memory.claude.*. Resolved per-run so the index reflects the
		// current MEMORY.md. ok=false when disabled or inert.
		if claudeMem != nil {
			if idx, ct, ok := claudeMem(); ok {
				builder = builder.WithClaudeMemory(idx, ct)
			}
		}
		agent, choice, err := builder.BuildAgent(agentID)
		if err != nil {
			return fail("build", err)
		}

		sess, err := agent.NewSessionWithHistory(ChatMessagesToJess(priorHistory))
		if err != nil {
			return fail("session", err)
		}

		promptCtx := memory.WithSource(ctx, memory.Source{
			SessionID: sessionKey,
			MessageID: runID,
			Tool:      "remember",
			Reason:    "model decided",
		})

		run, err := sess.Prompt(promptCtx, userText)
		if err != nil {
			return fail("prompt", err)
		}

		for ev := range run.Events() {
			adapter.Handle(ev)
		}
		res, runErr := run.Wait()
		// Prefer the assistant text from Wait's Result; fall back to the
		// accumulated streamed deltas when Wait returns no assistant message,
		// so FinalText (persisted to ChatStore) matches what the EventSink saw
		// via adapter.Finalize (which falls back to the same accumulator).
		final := lastAssistantText(res.Messages)
		if final == "" {
			acc, _ := adapter.Snapshot()
			final = acc
		}
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
