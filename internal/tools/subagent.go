package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/guygrigsby/talon/internal/subagents"
	"github.com/guygrigsby/talon/internal/talonpath"
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
	paths  talonpath.Paths
}

func (t *subagentTool) Name() string { return "subagent" }

func (t *subagentTool) Description() string {
	base := "Delegate work to a specialized subagent when its description matches the task. Pass the exact subagent id and a complete task prompt. The subagent runs its own loop and returns its final reply. Recursion is depth-limited."
	defs := t.availableSubagents()
	if len(defs) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString(" Available subagents: ")
	for i, def := range defs {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(def.ID)
		if def.Description != "" {
			b.WriteString(" - ")
			b.WriteString(def.Description)
		}
		if i == 11 && len(defs) > 12 {
			b.WriteString("; plus more from the agents list")
			break
		}
	}
	return b.String()
}

func (t *subagentTool) ParametersSchema() json.RawMessage {
	agentID := map[string]any{
		"type":        "string",
		"description": "Target subagent id from the agents list.",
	}
	defs := t.availableSubagents()
	if len(defs) > 0 {
		enum := make([]string, 0, len(defs))
		for _, def := range defs {
			enum = append(enum, def.ID)
		}
		agentID["enum"] = enum
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agentId": agentID,
			"prompt": map[string]any{
				"type":        "string",
				"description": "The complete task or question to hand to the subagent.",
			},
		},
		"required": []string{"agentId", "prompt"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
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

func (t *subagentTool) availableSubagents() []subagents.Definition {
	if t.paths.Talon.Dir == "" {
		return nil
	}
	defs, err := subagents.LoadDir(t.paths.Talon.SubagentsDir())
	if err != nil {
		return nil
	}
	return defs
}
