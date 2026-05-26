package subagents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile_ParsesOpencodeMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code-review.md")
	raw := `---
description: Reviews code for regressions.
model: anthropic/claude-sonnet-4-6
tools: [read, grep, edit]
---
You review code.
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	def, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if def.ID != "code-review" || def.Name != "Code Review" {
		t.Fatalf("identity fields wrong: %+v", def)
	}
	if def.Description != "Reviews code for regressions." {
		t.Fatalf("description = %q", def.Description)
	}
	if def.Model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("model = %q", def.Model)
	}
	if len(def.Tools) != 3 || def.Tools[0] != "edit" || def.Tools[1] != "grep" || def.Tools[2] != "read" {
		t.Fatalf("tools = %+v", def.Tools)
	}
	if def.Prompt != "You review code." {
		t.Fatalf("prompt = %q", def.Prompt)
	}
}

func TestLoadDir_SkipsDisabledAndNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "enabled.md"), []byte("---\nmodel: openai/gpt-4o-mini\n---\nEnabled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disabled.md"), []byte("---\ndisabled: true\n---\nDisabled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}

	defs, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].ID != "enabled" {
		t.Fatalf("defs = %+v", defs)
	}
}
