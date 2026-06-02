package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// The toolgate command resolves the agent workspace from a real on-disk TOML
// config (the same MergedBytes path the gate uses), so the displayed grant is
// scoped — not the unscoped grant an empty workspace would produce.
func TestToolgateWorkspace_ResolvesFromRealTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	rj := `{"agents":{"defaults":{"model":{"primary":"openai/gpt"},"workspace":"/tmp/mainws"},"list":[{"id":"main"}]}}`
	cfg, err := talonconfig.FromRuntimeJSON([]byte(rj))
	if err != nil {
		t.Fatalf("FromRuntimeJSON: %v", err)
	}
	if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	merged, err := config.MergedBytes(talonpath.Paths{Talon: talonpath.Layer{Dir: dir, Config: cfgPath}})
	if err != nil {
		t.Fatalf("MergedBytes: %v", err)
	}
	if ws := toolgateWorkspace(merged, "main"); ws != "/tmp/mainws" {
		t.Fatalf("toolgateWorkspace = %q, want /tmp/mainws (gate would run unscoped otherwise)", ws)
	}
}
