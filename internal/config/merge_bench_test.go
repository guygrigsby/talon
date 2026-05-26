package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// BenchmarkMergedBytes_TalonOnly is the common path: ~/.talon/config.toml read
// and adapted to the gateway JSON view.
func BenchmarkMergedBytes_TalonOnly(b *testing.B) {
	dir := b.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg, err := talonconfig.FromRuntimeJSON([]byte(typicalConfig))
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o644); err != nil {
		b.Fatal(err)
	}
	paths := talonpath.Paths{
		Talon: talonpath.Layer{Dir: dir, Config: cfgPath},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := MergedBytes(paths); err != nil {
			b.Fatal(err)
		}
	}
}

// typicalConfig is roughly the size of a busy config with ~3 agents
// and a modest set of plugin entries. Big enough that
// JSON parse cost shows up but not pathologically large.
const typicalConfig = `{
  "agents": {
    "list": [
      {"id": "main", "model": "openai/gpt-4o", "workspace": "/home/u/.talon"},
      {"id": "research", "model": "anthropic/claude-sonnet-4-5", "workspace": "/home/u/work/research"},
      {"id": "deep", "model": "openai/o3-mini", "workspace": "/home/u/work/deep"}
    ],
    "defaults": {"workspace": "/home/u/.talon"}
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
