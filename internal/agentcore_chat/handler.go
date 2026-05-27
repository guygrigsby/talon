package agentcore_chat

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/talonpath"
)

// Handler is the agentcore-based replacement for legacy
// `server.ChatHandler` chat semantics. One handler per gateway
// process; each Run call builds its own short-lived agent for the
// turn (agentcore.Agent is stateful, but talon's per-session
// history lives in the existing ChatStore, not the agent).
//
// This remains a package-level entry point for direct callers; the
// gateway path injects BuildAgent through server.AgentcoreRunFn.
type Handler struct {
	paths talonpath.Paths
	// configReader returns the merged config bytes. Injected so
	// tests can pass a fixed config without going through
	// config.MergedBytes. Production callers pass MergedBytesFn.
	configReader func() ([]byte, error)
}

// NewHandler constructs a Handler.
func NewHandler(paths talonpath.Paths, configReader func() ([]byte, error)) *Handler {
	return &Handler{paths: paths, configReader: configReader}
}

// RunRequest carries the per-call inputs for one chat turn.
type RunRequest struct {
	// AgentID picks which agents.list[] entry's config to use.
	// Empty defaults to "main".
	AgentID string
	// Prompt is the user's message text. Required.
	Prompt string
	// History is the prior conversation, oldest first. Passed
	// through to the agent so it has multi-turn context. Empty
	// for the first turn of a session.
	History []agentcore.AgentMessage
	// Sink receives events. Required.
	Sink EventSink
}

// RunResult is metadata about the completed turn.
type RunResult struct {
	// FinalText is the assistant's full visible reply (matches the
	// last sink.Final call).
	FinalText string
	// Model is the resolved model used for this turn.
	Model ModelChoice
	// TotalUsage from agentcore — input/output tokens consumed.
	Usage agentcore.Usage
	// NewMessages are messages the agent added during the run
	// (assistant turns + tool results). Caller appends to its own
	// history store.
	NewMessages []agentcore.Message
}

// Run executes one chat turn: build agent, subscribe to events,
// prompt, block until idle, return.
//
// The sink receives events synchronously during the run. The agent
// is constructed fresh each call to keep state outside the package
// boundary; multi-turn context is supplied via RunRequest.History.
func (h *Handler) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if req.Sink == nil {
		return nil, fmt.Errorf("agentcore_chat: RunRequest.Sink is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("agentcore_chat: RunRequest.Prompt is required")
	}

	merged, err := h.configReader()
	if err != nil {
		return nil, fmt.Errorf("read merged config: %w", err)
	}

	agent, choice, err := NewBuilder(merged, h.paths).BuildAgent(req.AgentID)
	if err != nil {
		slog.Error("agentcore build-agent failed", "agent", req.AgentID, "err", err)
		req.Sink.Error("build-agent", err.Error())
		return nil, err
	}

	// Hydrate prior conversation. Build expects assistant + user
	// messages; tool results land via the agent's own loop, not
	// from history.
	if len(req.History) > 0 {
		if err := agent.SetMessages(req.History); err != nil {
			slog.Error("agentcore seed-history failed", "agent", req.AgentID, "err", err)
			req.Sink.Error("seed-history", err.Error())
			return nil, fmt.Errorf("seed history: %w", err)
		}
	}

	adapter := NewEventAdapter(req.Sink)
	unsubscribe := agent.Subscribe(func(ev agentcore.Event) {
		adapter.Handle(ev)
	})
	defer unsubscribe()

	if err := agent.Prompt(req.Prompt); err != nil {
		slog.Error("agentcore prompt failed", "agent", req.AgentID, "err", err)
		req.Sink.Error("prompt", err.Error())
		return nil, fmt.Errorf("prompt: %w", err)
	}

	// Block until the agent reaches idle, honoring ctx cancellation.
	done := make(chan struct{})
	go func() {
		agent.WaitForIdle()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		agent.Abort()
		// Wait for the abort to flush events through the sink
		// before returning. Sink.Error has already been called by
		// the adapter when agent_end fires with an error.
		<-done
		return nil, ctx.Err()
	}

	final, _ := adapter.Snapshot()
	return &RunResult{
		FinalText:   final,
		Model:       choice,
		Usage:       agent.TotalUsage(),
		NewMessages: agent.ExportMessages(),
	}, nil
}
