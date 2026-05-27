// Package talonpath resolves Talon's local state paths.
package talonpath

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	configFilename = "config.toml"
	lastGoodSuffix = ".last-good"
	bakSuffix      = ".bak"
)

// Paths describes Talon's state root and primary config file.
type Paths struct {
	Talon Layer
}

// Layer is a state directory plus its config path.
type Layer struct {
	Dir    string
	Config string
}

// DefaultPaths returns the default Talon paths, honoring Talon-owned env
// overrides only.
func DefaultPaths() Paths {
	talonDir := resolveStateDir("TALON_STATE_DIR", ".talon")
	return Paths{
		Talon: Layer{
			Dir:    talonDir,
			Config: resolveConfigPath(talonDir),
		},
	}
}

func resolveStateDir(envKey, defaultName string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return expandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return defaultName
	}
	return filepath.Join(home, defaultName)
}

func resolveConfigPath(stateDir string) string {
	if v := strings.TrimSpace(os.Getenv("TALON_CONFIG_PATH")); v != "" {
		return expandHome(v)
	}
	return filepath.Join(stateDir, configFilename)
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// ConfigBackupPath returns the rotation-ring backup at index n for this
// layer's config. n=0 returns the un-numbered ".bak"; n=1..N return ".bak.N".
func (l Layer) ConfigBackupPath(n int) string {
	if n <= 0 {
		return l.Config + bakSuffix
	}
	return l.Config + bakSuffix + "." + strconv.Itoa(n)
}

// LastGoodPath returns the "<config>.last-good" sidecar path.
func (l Layer) LastGoodPath() string { return l.Config + lastGoodSuffix }

// LogsDir returns "<Dir>/logs".
func (l Layer) LogsDir() string { return filepath.Join(l.Dir, "logs") }

// ConfigAuditLogPath returns the JSONL audit-log path for this layer.
func (l Layer) ConfigAuditLogPath() string {
	return filepath.Join(l.LogsDir(), "config-audit.jsonl")
}

// AgentAuditLogPath returns the JSONL agent-action audit-log path.
func (l Layer) AgentAuditLogPath() string {
	return filepath.Join(l.LogsDir(), "agent-audit.jsonl")
}

// CredentialsDir returns "<Dir>/credentials".
func (l Layer) CredentialsDir() string { return filepath.Join(l.Dir, "credentials") }

// IdentityDir returns "<Dir>/identity".
func (l Layer) IdentityDir() string { return filepath.Join(l.Dir, "identity") }

// LocksDir returns "<Dir>/locks".
func (l Layer) LocksDir() string { return filepath.Join(l.Dir, "locks") }

// AgentsDir returns "<Dir>/agents".
func (l Layer) AgentsDir() string { return filepath.Join(l.Dir, "agents") }

// AgentDir returns "<Dir>/agents/<id>".
func (l Layer) AgentDir(id string) string { return filepath.Join(l.AgentsDir(), id) }

// SubagentsDir returns "<Dir>/subagents".
func (l Layer) SubagentsDir() string { return filepath.Join(l.Dir, "subagents") }

// PluginsDir returns "<Dir>/plugins".
func (l Layer) PluginsDir() string { return filepath.Join(l.Dir, "plugins") }

// CacheDir returns "<Dir>/cache".
func (l Layer) CacheDir() string { return filepath.Join(l.Dir, "cache") }

// SchemaCachePath returns the cached config schema path for this layer.
func (l Layer) SchemaCachePath() string {
	return filepath.Join(l.CacheDir(), "config-schema.json")
}
