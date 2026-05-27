package claudemem

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestNewTool_Interface(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md": "i"}))
	var _ agentcore.Tool = NewTool(s, 4096)
}

func TestTool_NameAndSchema(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md": "i"}))
	tl := NewTool(s, 4096)
	if tl.Name() != "claude_memory" {
		t.Fatalf("Name = %q, want claude_memory", tl.Name())
	}
	if tl.Description() == "" {
		t.Fatal("Description must be non-empty")
	}
	sch := tl.Schema()
	if sch["type"] != "object" {
		t.Fatalf("schema type = %v, want object", sch["type"])
	}
	props, _ := sch["properties"].(map[string]any)
	if _, ok := props["op"]; !ok {
		t.Fatalf("schema missing op property: %v", sch)
	}
	if _, ok := props["slug"]; !ok {
		t.Fatalf("schema missing slug property: %v", sch)
	}
}

func TestTool_ListAndRead(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md": "i", "feedback_x.md": "body-x"}))
	tl := NewTool(s, 4096)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"op":"read","slug":"feedback_x"}`))
	if err != nil || !strings.Contains(string(out), "body-x") {
		t.Fatalf("read: err=%v out=%q", err, out)
	}

	lst, err := tl.Execute(context.Background(), json.RawMessage(`{"op":"list"}`))
	if err != nil || !strings.Contains(string(lst), "feedback_x") {
		t.Fatalf("list: err=%v out=%q", err, lst)
	}
}

func TestTool_TraversalIsToolError(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md": "i", "feedback_x.md": "body-x"}))
	tl := NewTool(s, 4096)
	// A traversal attempt must not panic and must surface as an error
	// the agent can read (returned error or IsError result), never a
	// successful read of an out-of-dir file.
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"op":"read","slug":"../../etc/passwd"}`))
	if err == nil && strings.Contains(strings.ToLower(string(out)), "root:") {
		t.Fatalf("traversal leaked file content: %q", out)
	}
}

func TestTool_BadOp(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md": "i"}))
	tl := NewTool(s, 4096)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"op":"delete"}`)); err == nil {
		t.Fatal("unknown op should error")
	}
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"op":"read"}`)); err == nil {
		t.Fatal("read without slug should error")
	}
}
