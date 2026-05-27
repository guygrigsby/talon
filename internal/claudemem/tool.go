package claudemem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool is the read-only `claude_memory` agentcore.Tool. It exposes two
// operations over a path-confined Store: `list` (memory slugs) and
// `read` (one memory's content). All confinement lives in the Store
// (see Read), so the tool cannot reach outside the configured dir.
type Tool struct {
	store   *Store
	maxRead int
}

// NewTool builds the claude_memory tool over store, bounding each read
// to maxRead bytes (0 = uncapped).
func NewTool(store *Store, maxRead int) *Tool {
	return &Tool{store: store, maxRead: maxRead}
}

// Name satisfies agentcore.Tool.
func (t *Tool) Name() string { return "claude_memory" }

// Description satisfies agentcore.Tool.
func (t *Tool) Description() string {
	return "Read-only access to notes Claude has saved about the user and project " +
		"(preferences, conventions, project decisions). " +
		"Use op=list to see available memory slugs, then op=read with a slug to read " +
		"one memory's full content. Cannot write or read outside the saved-notes directory."
}

// Schema satisfies agentcore.Tool.
func (t *Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "read"},
				"description": "list = enumerate memory slugs; read = return one memory's content",
			},
			"slug": map[string]any{
				"type":        "string",
				"description": "Memory slug to read (required when op=read), e.g. \"feedback_x\". A bare filename stem, no path separators.",
			},
		},
		"required": []string{"op"},
	}
}

type toolArgs struct {
	Op   string `json:"op"`
	Slug string `json:"slug"`
}

// Execute satisfies agentcore.Tool. A bad op, missing slug, or
// path-confinement violation returns an error the agent reads as a tool
// failure — it never panics and never reads outside the configured dir.
func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args toolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("claude_memory: invalid args: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(args.Op)) {
	case "list":
		slugs, err := t.store.List()
		if err != nil {
			return nil, fmt.Errorf("claude_memory: list: %w", err)
		}
		out, err := json.Marshal(map[string]any{"slugs": slugs})
		if err != nil {
			return nil, fmt.Errorf("claude_memory: marshal list: %w", err)
		}
		return out, nil
	case "read":
		if strings.TrimSpace(args.Slug) == "" {
			return nil, fmt.Errorf("claude_memory: read requires a slug")
		}
		body, err := t.store.Read(args.Slug, t.maxRead)
		if err != nil {
			// Confinement and not-found errors surface to the agent as a
			// tool failure, not a panic and not a leaked file.
			return nil, fmt.Errorf("claude_memory: %w", err)
		}
		out, err := json.Marshal(map[string]any{"slug": args.Slug, "content": body})
		if err != nil {
			return nil, fmt.Errorf("claude_memory: marshal read: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("claude_memory: unknown op %q (want list or read)", args.Op)
	}
}
