package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/subagents"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// agentsTool returns the configured agent/subagent list so the model doesn't
// have to read raw config files.
type agentsTool struct {
	paths talonpath.Paths
}

// NewAgentsTool returns the merged-agents-list tool. It needs paths so it
// can resolve the layered overlay; that's why it isn't auto-registered by
// New() like the workspace-only builtins.
func NewAgentsTool(paths talonpath.Paths) Tool {
	return &agentsTool{paths: paths}
}

func (t *agentsTool) Name() string { return "agents" }

func (t *agentsTool) Description() string {
	return "List the main chat agent and file-backed subagents with ids, models, and delegation guidance."
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
	defaultWorkspace := gjson.GetBytes(merged, "agents.defaults.workspace").Str

	var lines []string
	seen := map[string]bool{}
	gjson.GetBytes(merged, "agents.list").ForEach(func(_, agent gjson.Result) bool {
		id := agent.Get("id").Str
		if id == "" {
			return true
		}
		seen[id] = true
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
		if ws == "" {
			ws = defaultWorkspace
		}
		lines = append(lines,
			fmt.Sprintf("- kind=main id=%s name=%q model=%q workspace=%q", id, name, primary, ws))
		return true
	})
	defs, err := subagents.LoadDir(t.paths.Talon.SubagentsDir())
	if err != nil {
		return "", fmt.Errorf("agents: %w", err)
	}
	for _, def := range defs {
		if seen[def.ID] {
			continue
		}
		model := def.Model
		if model == "" {
			model = defaultPrimary
		}
		tools := strings.Join(def.Tools, ",")
		lines = append(lines,
			fmt.Sprintf("- kind=subagent id=%s name=%q model=%q tools=%q use_when=%q", def.ID, def.Name, model, tools, def.Description))
	}
	if len(lines) == 0 {
		return "(no agents configured)", nil
	}
	return strings.Join(lines, "\n"), nil
}
