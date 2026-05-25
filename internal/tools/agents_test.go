package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/openclaw"
)

func TestAgentsTool_MergesTalonOverlayOverOpenclaw(t *testing.T) {
	stateDir := t.TempDir()
	openclawDir := filepath.Join(stateDir, ".openclaw")
	talonDir := filepath.Join(stateDir, ".talon")
	for _, d := range []string{openclawDir, talonDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// openclaw layer: main + coding (no chat).
	openclawCfg := `{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}},
			"list": [
				{"id":"main","name":"main","model":{"primary":"openai/gpt-5.4-mini"}},
				{"id":"coding","name":"coding","model":{"primary":"anthropic/claude-opus-4-7"},"workspace":"/tmp/ws-coding"}
			]
		}
	}`
	talonCfg := `{
		"agents": {
			"list": [
				{"id":"chat","model":"lmstudio/dolphin","workspace":"/tmp/ws-chat"}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(openclawDir, "openclaw.json"), []byte(openclawCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(talonDir, "openclaw.json"), []byte(talonCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TALON_STATE_DIR", talonDir)
	t.Setenv("OPENCLAW_STATE_DIR", openclawDir)
	t.Setenv("TALON_CONFIG_PATH", filepath.Join(talonDir, "openclaw.json"))
	t.Setenv("OPENCLAW_CONFIG_PATH", filepath.Join(openclawDir, "openclaw.json"))

	paths := openclaw.DefaultPaths()
	// DefaultPaths() now skips the openclaw layer; this test
	// exercises the merge semantics specifically, so opt back in.
	paths.SkipOpenclaw = false
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
	// Chat agent must carry the talon-overlay workspace.
	if !strings.Contains(out, `workspace="/tmp/ws-chat"`) {
		t.Errorf("chat workspace missing in output:\n%s", out)
	}
	// Chat agent's model resolves the string-shorthand `model` field.
	if !strings.Contains(out, `model="lmstudio/dolphin"`) {
		t.Errorf("chat model not resolved from string shorthand:\n%s", out)
	}
}

func TestAgentsTool_RegisteredByNewWithSubagentAndPaths(t *testing.T) {
	stateDir := t.TempDir()
	openclawDir := filepath.Join(stateDir, ".openclaw")
	talonDir := filepath.Join(stateDir, ".talon")
	for _, d := range []string{openclawDir, talonDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(talonDir, "openclaw.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TALON_CONFIG_PATH", filepath.Join(talonDir, "openclaw.json"))
	t.Setenv("OPENCLAW_CONFIG_PATH", filepath.Join(openclawDir, "openclaw.json"))

	paths := openclaw.DefaultPaths()
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
