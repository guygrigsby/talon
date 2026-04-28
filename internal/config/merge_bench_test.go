package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guygrigsby/talon/internal/openclaw"
)

// BenchmarkMergedBytes_TalonOnly is the common path: openclaw layer
// disabled, only ~/.talon/openclaw.json read + canonicalized. Runs
// once per chat.send via the agent resolver chain, so its cost
// matters.
func BenchmarkMergedBytes_TalonOnly(b *testing.B) {
	dir := b.TempDir()
	cfgPath := filepath.Join(dir, "openclaw.json")
	if err := os.WriteFile(cfgPath, []byte(typicalConfig), 0o644); err != nil {
		b.Fatal(err)
	}
	paths := openclaw.Paths{
		Talon:        openclaw.Layer{Dir: dir, Config: cfgPath},
		SkipOpenclaw: true,
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := MergedBytes(paths); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMergedBytes_BothLayers exercises the deep-merge path:
// both ~/.openclaw and ~/.talon present, requires JSON parse + merge
// + canonicalize. Worst-case for the per-call cost.
func BenchmarkMergedBytes_BothLayers(b *testing.B) {
	dir := b.TempDir()
	openclawDir := filepath.Join(dir, "openclaw")
	talonDir := filepath.Join(dir, "talon")
	if err := os.MkdirAll(openclawDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(talonDir, 0o755); err != nil {
		b.Fatal(err)
	}
	openclawCfg := filepath.Join(openclawDir, "openclaw.json")
	talonCfg := filepath.Join(talonDir, "openclaw.json")
	if err := os.WriteFile(openclawCfg, []byte(typicalConfig), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(talonCfg, []byte(typicalOverlay), 0o644); err != nil {
		b.Fatal(err)
	}
	paths := openclaw.Paths{
		Openclaw: openclaw.Layer{Dir: openclawDir, Config: openclawCfg},
		Talon:    openclaw.Layer{Dir: talonDir, Config: talonCfg},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := MergedBytes(paths); err != nil {
			b.Fatal(err)
		}
	}
}

// typicalConfig is roughly the size of a real openclaw.json with
// ~3 agents and a modest set of extension entries. Big enough that
// JSON parse cost shows up but not pathologically large.
const typicalConfig = `{
  "agents": {
    "list": [
      {"id": "main", "model": "openai/gpt-4o", "workspace": "/home/u/.openclaw/workspace"},
      {"id": "research", "model": "anthropic/claude-sonnet-4-5", "workspace": "/home/u/work/research"},
      {"id": "deep", "model": "openai/o3-mini", "workspace": "/home/u/work/deep"}
    ],
    "defaults": {"workspace": "/home/u/.openclaw/workspace"}
  },
  "models": {
    "providers": {
      "openai": {"defaultModel": "gpt-4o"},
      "anthropic": {"defaultModel": "claude-sonnet-4-5"}
    }
  },
  "plugins": {
    "entries": {
      "telegram": {"enabled": false},
      "brave": {"enabled": false},
      "memory-core": {"enabled": true},
      "active-memory": {"enabled": true}
    }
  },
  "channels": {},
  "gateway": {"auth": {"mode": "token"}}
}`

const typicalOverlay = `{
  "agents": {
    "list": [
      {"id": "main", "model": "openai/gpt-4-turbo"}
    ]
  },
  "gateway": {"auth": {"mode": "none"}}
}`
