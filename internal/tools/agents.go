package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
)

// agentsTool returns the merged agent list from talon's layered config so
// the model doesn't have to read raw config files. A direct `read` of
// ~/.openclaw/openclaw.json only surfaces the openclaw layer; agents
// defined solely in ~/.talon/openclaw.json (e.g. a local "chat" persona)
// stay invisible. This tool calls config.MergedBytes so the talon overlay
// is included.
type agentsTool struct {
	paths openclaw.Paths
}

// NewAgentsTool returns the merged-agents-list tool. It needs paths so it
// can resolve the layered overlay; that's why it isn't auto-registered by
// New() like the workspace-only builtins.
func NewAgentsTool(paths openclaw.Paths) Tool {
	return &agentsTool{paths: paths}
}

func (t *agentsTool) Name() string { return "agents" }

func (t *agentsTool) Description() string {
	return "List all configured agents (id, name, model, workspace) from the merged talon+openclaw config. Use this instead of reading openclaw.json directly — that misses agents defined only in the talon overlay."
}

func (t *agentsTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (t *agentsTool) Run(_ context.Context, _ json.RawMessage) (string, error) {
	merged, err := config.MergedBytes(t.paths)
	if err != nil {
		return "", fmt.Errorf("agents: %w", err)
	}
	defaultPrimary := gjson.GetBytes(merged, "agents.defaults.model.primary").Str

	var lines []string
	gjson.GetBytes(merged, "agents.list").ForEach(func(_, agent gjson.Result) bool {
		id := agent.Get("id").Str
		if id == "" {
			return true
		}
		name := agent.Get("name").Str
		if name == "" {
			name = id
		}
		// Mirrors configAgentResolver/handleAgentsList model precedence.
		primary := defaultPrimary
		if v := agent.Get("model.primary"); v.Exists() && v.Str != "" {
			primary = v.Str
		} else if v := agent.Get("model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
			primary = v.Str
		}
		ws := agent.Get("workspace").Str
		lines = append(lines,
			fmt.Sprintf("- id=%s name=%q model=%q workspace=%q", id, name, primary, ws))
		return true
	})
	if len(lines) == 0 {
		return "(no agents configured)", nil
	}
	return strings.Join(lines, "\n"), nil
}
