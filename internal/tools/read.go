package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type readTool struct{ ws string }

func (t *readTool) Name() string { return "read" }

func (t *readTool) Description() string {
	return "Read a file from the workspace. Optionally limit to a line range."
}

func (t *readTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Workspace-relative or absolute path under the workspace."},
			"start": {"type": "integer", "description": "1-indexed start line. Optional.", "minimum": 1},
			"limit": {"type": "integer", "description": "Maximum lines to return. Optional.", "minimum": 1}
		},
		"required": ["path"]
	}`)
}

func (t *readTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Path  string `json:"path"`
		Start int    `json:"start"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("read: path is required")
	}
	abs, err := resolveInWorkspace(t.ws, p.Path)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.Path, err)
	}
	if p.Start <= 0 && p.Limit <= 0 {
		return string(body), nil
	}
	lines := strings.Split(string(body), "\n")
	start := p.Start
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "", nil
	}
	end := len(lines)
	if p.Limit > 0 && start-1+p.Limit < end {
		end = start - 1 + p.Limit
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
