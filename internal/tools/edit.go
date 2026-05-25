package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type editTool struct{ ws string }

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return "Replace an exact string match in a file. Errors if the match is not unique or not found."
}

func (t *editTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"old": {"type": "string", "description": "Exact string to find. Must match exactly once."},
			"new": {"type": "string"}
		},
		"required": ["path", "old", "new"]
	}`)
}

func (t *editTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	if p.Path == "" || p.Old == "" {
		return "", fmt.Errorf("edit: path and old are required")
	}
	abs, err := resolveInWorkspace(t.ws, p.Path)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", p.Path, err)
	}
	count := strings.Count(string(body), p.Old)
	if count == 0 {
		return "", fmt.Errorf("edit %s: string not found", p.Path)
	}
	if count > 1 {
		return "", fmt.Errorf("edit %s: 'old' matches %d times; provide more surrounding context to make it unique", p.Path, count)
	}
	updated := strings.Replace(string(body), p.Old, p.New, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit %s: %w", p.Path, err)
	}
	return fmt.Sprintf("edited %s", p.Path), nil
}
