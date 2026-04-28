package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guygrigsby/talon/internal/memory"
)

type rememberTool struct{ ws string }

func (t *rememberTool) Name() string { return "remember" }

func (t *rememberTool) Description() string {
	return "Append a durable note to the agent's daily memory journal at <workspace>/memory/YYYY-MM-DD.md. Use markdown. Notes survive across sessions and feed back into future system prompts."
}

func (t *rememberTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "description": "The note to record. Markdown is fine."}
		},
		"required": ["text"]
	}`)
}

func (t *rememberTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("remember: %w", err)
	}
	if err := memory.Append(t.ws, p.Text); err != nil {
		return "", err
	}
	return "remembered", nil
}
