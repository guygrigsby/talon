package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func writeAgentsFixture(t *testing.T, runtimeJSON string) talonpath.Paths {
	t.Helper()
	dir := t.TempDir()
	cfg, err := talonconfig.FromRuntimeJSON([]byte(runtimeJSON))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
		t.Fatal(err)
	}
	return talonpath.Paths{Talon: talonpath.Layer{Dir: dir, Config: cfgPath}}
}

func TestAgentsTool_ListsConfiguredAgents(t *testing.T) {
	paths := writeAgentsFixture(t, `{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}},
			"list": [
				{"id":"main","name":"main","model":{"primary":"openai/gpt-5.4-mini"}}
			]
		}
	}`)
	if err := os.MkdirAll(paths.Talon.SubagentsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Talon.SubagentsDir(), "coding.md"), []byte(`---
description: Code work
model: anthropic/claude-opus-4-7
---
Code work.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Talon.SubagentsDir(), "chat.md"), []byte(`---
description: Local chat model
model: lmstudio/dolphin
---
Chat work.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewAgentsTool(paths)

	out, err := tool.Run(t.Context(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"id=main", "id=coding", "id=chat"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `model="lmstudio/dolphin"`) {
		t.Errorf("chat model not resolved from string shorthand:\n%s", out)
	}
}

func TestAgentsTool_RegisteredByNewWithSubagentAndPaths(t *testing.T) {
	paths := writeAgentsFixture(t, `{"agents":{"list":[{"id":"main"}]}}`)
	r := NewWithSubagentAndPaths(t.TempDir(), nil, paths)

	found := false
	for _, n := range r.Names() {
		if n == "agents" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("agents tool not registered: got %v", r.Names())
	}
}
