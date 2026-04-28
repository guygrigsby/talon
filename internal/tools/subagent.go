package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SubagentRunner is the indirection that lets the subagent tool invoke
// another agent's full chat.send loop without tools depending on the
// server package directly. The gateway wires server.ChatHandler in here.
type SubagentRunner interface {
	RunInline(ctx context.Context, agentID, message string) (string, error)
}

// subagentDepthKey is the context key used to bound recursion. Each
// subagent invocation increments the depth before delegating; tools that
// exceed maxSubagentDepth refuse so a runaway model can't spin agents
// forever (each level still has its own MaxToolIterations cap, but the
// depth limit catches the cross-level case).
type subagentDepthKey struct{}

const maxSubagentDepth = 3

func subagentDepth(ctx context.Context) int {
	if v, ok := ctx.Value(subagentDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func withSubagentDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, subagentDepthKey{}, d)
}

type subagentTool struct {
	runner SubagentRunner
}

func (t *subagentTool) Name() string { return "subagent" }

func (t *subagentTool) Description() string {
	return "Delegate work to another agent. Use when a task is better handled by a specialized agent (e.g. coding for code-heavy tasks, research for summaries). The subagent runs its own multi-turn loop and returns its final reply as a string. Subagent recursion is depth-limited."
}

func (t *subagentTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"agentId": {"type": "string", "description": "Target agent id (e.g. 'coding', 'research', 'deepwork')."},
			"prompt":  {"type": "string", "description": "The task or question to hand to the subagent. Be specific."}
		},
		"required": ["agentId", "prompt"]
	}`)
}

func (t *subagentTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if t.runner == nil {
		return "", errors.New("subagent: no runner configured")
	}
	depth := subagentDepth(ctx)
	if depth >= maxSubagentDepth {
		return "", fmt.Errorf("subagent: depth limit %d reached (current=%d) — agents are calling agents in a loop", maxSubagentDepth, depth)
	}
	var p struct {
		AgentID string `json:"agentId"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("subagent: %w", err)
	}
	if strings.TrimSpace(p.AgentID) == "" || strings.TrimSpace(p.Prompt) == "" {
		return "", errors.New("subagent: agentId and prompt are required")
	}
	return t.runner.RunInline(withSubagentDepth(ctx, depth+1), p.AgentID, p.Prompt)
}
