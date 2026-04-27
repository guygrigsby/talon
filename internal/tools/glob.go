package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type globTool struct{ ws string }

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return "List workspace paths matching a glob pattern. Supports * and ?; not recursive (use ** in nested patterns via doublestar later)."
}

func (t *globTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Workspace-relative glob, e.g. \"*.go\" or \"cmd/*/*.go\"."}
		},
		"required": ["pattern"]
	}`)
}

func (t *globTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	abs, err := resolveInWorkspace(t.ws, p.Pattern)
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(abs)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := make([]string, 0, len(matches))
	wsPrefix := filepath.Clean(t.ws) + string(filepath.Separator)
	for _, m := range matches {
		// Render as workspace-relative for tidy model output.
		out = append(out, strings.TrimPrefix(m, wsPrefix))
	}
	return strings.Join(out, "\n"), nil
}
