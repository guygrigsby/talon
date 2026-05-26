// buildAgentcoreRunner returns the AgentcoreRunFn the server's
// ChatHandler invokes for providers routed through agentcore.
// Lives in cmd/talon (not internal/server) so internal/server
// doesn't take a transitive dep on agentcore + LiteLLM. The fn
// captures the gateway's Paths so each invocation reads fresh
// merged config.

package main

import (
	"context"
	"fmt"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/agentcore_chat"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/server"
)

func buildAgentcoreRunner(paths openclaw.Paths, mem *server.MemoryConfig) server.AgentcoreRunFn {
	return func(
		ctx context.Context,
		agentID, sessionKey, runID, userText, modelOverride string,
		emitText func(seq int, state, full, delta string),
		emitToolStart func(toolCallID, name, args string),
		emitToolResult func(toolCallID, name, output string, isErr bool),
		emitError func(seq int, kind, msg string),
	) (string, error) {
		sink := &gatewayEventSink{
			text:       emitText,
			toolStart:  emitToolStart,
			toolResult: emitToolResult,
			err:        emitError,
		}

		merged, err := config.MergedBytes(paths)
		if err != nil {
			sink.Error("merged-config", err.Error())
			return "", fmt.Errorf("read merged config: %w", err)
		}

		builder := agentcore_chat.NewBuilder(merged, paths)
		if modelOverride != "" {
			builder = builder.WithModelOverride(modelOverride)
		}
		if mem != nil {
			builder = builder.WithMemory(mem.Store, mem.Recaller)
		}
		agent, _, err := builder.BuildAgent(agentID)
		if err != nil {
			sink.Error("build-agent", err.Error())
			return "", err
		}

		adapter := agentcore_chat.NewEventAdapter(sink)
		unsub := agent.Subscribe(func(ev agentcore.Event) { adapter.Handle(ev) })
		defer unsub()

		if err := agent.Prompt(userText); err != nil {
			sink.Error("prompt", err.Error())
			return "", err
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
			return "", ctx.Err()
		}

		final, _ := adapter.Snapshot()
		return final, nil
	}
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
