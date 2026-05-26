package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/guygrigsby/talon/internal/netutil"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func writeFixture(t *testing.T, body string) talonpath.Paths {
	t.Helper()
	dir := t.TempDir()
	talonDir := filepath.Join(dir, "talon")
	if err := os.MkdirAll(talonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := talonconfig.FromRuntimeJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(talonDir, "config.toml")
	if err := os.WriteFile(configPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
		t.Fatal(err)
	}
	return talonpath.Paths{
		Talon: talonpath.Layer{Dir: talonDir, Config: configPath},
	}
}

func writeSubagent(t *testing.T, paths talonpath.Paths, name, body string) {
	t.Helper()
	dir := paths.Talon.SubagentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Main inherits from defaults; subagents are file-backed task/model profiles.
const fixtureRealisticAgents = `{
	"agents": {
		"defaults": {
			"model": {
				"primary": "openai/gpt-5.4-mini",
				"fallbacks": ["anthropic/claude-opus-4-7"]
			}
		},
		"list": [
			{"id": "main", "tools": {"profile": "full"}},
			{"id": "coding", "model": "anthropic/claude-opus-4-7"},
			{"id": "future", "model": {"primary": "deepseek/deepseek-reasoner"}}
		]
	}
}`

func TestConfigAgentResolver_FallsBackToDefaultsWhenAgentHasNoModel(t *testing.T) {
	r := &configAgentResolver{paths: writeFixture(t, fixtureRealisticAgents)}
	got, err := r.PrimaryModel("main")
	if err != nil {
		t.Fatal(err)
	}
	if got != "openai/gpt-5.4-mini" {
		t.Errorf("PrimaryModel(main) = %q, want openai/gpt-5.4-mini", got)
	}
}

func TestConfigAgentResolver_ReadsPerAgentStringShorthand(t *testing.T) {
	paths := writeFixture(t, fixtureRealisticAgents)
	writeSubagent(t, paths, "coding.md", `---
model: anthropic/claude-opus-4-7
---
Code work.
`)
	r := &configAgentResolver{paths: paths}
	got, err := r.PrimaryModel("coding")
	if err != nil {
		t.Fatal(err)
	}
	if got != "anthropic/claude-opus-4-7" {
		t.Errorf("PrimaryModel(coding) = %q, want anthropic/claude-opus-4-7", got)
	}
}

func TestConfigAgentResolver_ReadsPerAgentObjectForm(t *testing.T) {
	paths := writeFixture(t, fixtureRealisticAgents)
	writeSubagent(t, paths, "future.md", `---
model: deepseek/deepseek-reasoner
---
Future-looking work.
`)
	r := &configAgentResolver{paths: paths}
	got, err := r.PrimaryModel("future")
	if err != nil {
		t.Fatal(err)
	}
	if got != "deepseek/deepseek-reasoner" {
		t.Errorf("PrimaryModel(future) = %q, want deepseek/deepseek-reasoner", got)
	}
}

func TestConfigAgentResolver_AgentNotFound(t *testing.T) {
	r := &configAgentResolver{paths: writeFixture(t, fixtureRealisticAgents)}
	_, err := r.PrimaryModel("nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, server.ErrAgentNotFound) {
		t.Errorf("error should wrap ErrAgentNotFound: %v", err)
	}
}

func TestConfigAgentResolver_NoModelAndNoDefaultsErrors(t *testing.T) {
	r := &configAgentResolver{paths: writeFixture(t, `{
		"agents": {"list": [{"id": "main"}]}
	}`)}
	_, err := r.PrimaryModel("main")
	if err == nil || errors.Is(err, server.ErrAgentNotFound) {
		t.Errorf("expected non-AgentNotFound error for missing model, got %v", err)
	}
}

func TestRewriteLoopback(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		inContainer bool
		want        string
	}{
		{
			name:        "host: rewrite skipped outside container",
			in:          "http://localhost:1234/v1",
			inContainer: false,
			want:        "http://localhost:1234/v1",
		},
		{
			name:        "container: localhost → host.docker.internal",
			in:          "http://localhost:1234/v1",
			inContainer: true,
			want:        "http://host.docker.internal:1234/v1",
		},
		{
			name:        "container: 127.0.0.1 → host.docker.internal",
			in:          "http://127.0.0.1:1234/v1",
			inContainer: true,
			want:        "http://host.docker.internal:1234/v1",
		},
		{
			name:        "container: ::1 → host.docker.internal",
			in:          "http://[::1]:1234/v1",
			inContainer: true,
			want:        "http://host.docker.internal:1234/v1",
		},
		{
			name:        "container: LAN host left alone",
			in:          "http://10.0.0.5:1234/v1",
			inContainer: true,
			want:        "http://10.0.0.5:1234/v1",
		},
		{
			name:        "container: public hostname left alone",
			in:          "https://api.example.com/v1",
			inContainer: true,
			want:        "https://api.example.com/v1",
		},
		{
			name:        "container: malformed URL passes through unchanged",
			in:          "://broken",
			inContainer: true,
			want:        "://broken",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netutil.RewriteLoopback(tc.in, tc.inContainer); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigAgentResolver_ToolsEnabledDefaultTrue(t *testing.T) {
	r := &configAgentResolver{paths: writeFixture(t, `{
		"agents": {"list": [{"id": "main", "model": "openai/gpt-4o"}]}
	}`)}
	got, err := r.ToolsEnabled("main")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("default should be true; got false")
	}
}

func TestConfigAgentResolver_ToolsEnabledExplicitFalse(t *testing.T) {
	r := &configAgentResolver{paths: writeFixture(t, `{
		"agents": {"list": [{"id": "main", "tools": {"enabled": false}}]}
	}`)}
	got, err := r.ToolsEnabled("main")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("expected false for explicit tools.enabled=false")
	}
}
