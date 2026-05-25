package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type writeTool struct{ ws string }

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) Description() string {
	return "Create or overwrite a file in the workspace with the given content. " +
		"For code, notes, configs, drafts — anything the user wants persisted as " +
		"a file in their project. Do NOT use this to save facts to your own memory; " +
		"the `remember` tool is the only memory-persistence path. Writing to paths " +
		"like memory/, MEMORY.md, or memory/YYYY-MM-DD.md is a legacy pattern that " +
		"talon's runtime no longer reads — those writes will not become memory."
}

func (t *writeTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
}

func (t *writeTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("write: path is required")
	}
	abs, err := resolveInWorkspace(t.ws, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("write %s: mkdir: %w", p.Path, err)
	}
	if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
}
