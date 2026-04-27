// Package openclaw resolves the layered state directories that talon shares
// with the openclaw runtime.
//
// talon's state model is a two-layer overlay:
//
//   - ~/.openclaw — managed by openclaw; talon treats it as read-only.
//   - ~/.talon    — talon's own state; the only place talon writes.
//
// At read time the two layers are merged, with ~/.talon taking priority. The
// merge is performed against the parsed JSON of openclaw.json on each layer.
package openclaw

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	configFilename = "openclaw.json"
	lastGoodSuffix = ".last-good"
	bakSuffix      = ".bak"
)

// Paths describes the talon overlay and the openclaw fallback layer.
//
// Writes always target Talon. Reads merge Talon over Openclaw.
type Paths struct {
	Talon    Layer
	Openclaw Layer
	// SkipOpenclaw disables the openclaw fallback at read time. Tests and
	// "I want a fresh talon-only config" use this.
	SkipOpenclaw bool
}

// Layer is one half of the overlay — a state directory plus the resolved
// path of its openclaw.json. Config can diverge from filepath.Join(Dir, ...)
// when an env override (TALON_CONFIG_PATH or OPENCLAW_CONFIG_PATH) points
// at a config file outside the state dir.
type Layer struct {
	Dir    string // state dir (~/.talon, ~/.openclaw, or override)
	Config string // resolved openclaw.json path
}

// DefaultPaths returns the default layered paths, honoring env overrides.
func DefaultPaths() Paths {
	talonDir := resolveStateDir("TALON_STATE_DIR", ".talon")
	openclawDir := resolveStateDir("OPENCLAW_STATE_DIR", ".openclaw")
	return Paths{
		Talon: Layer{
			Dir:    talonDir,
			Config: resolveConfigPath("TALON_CONFIG_PATH", talonDir),
		},
		Openclaw: Layer{
			Dir:    openclawDir,
			Config: resolveConfigPath("OPENCLAW_CONFIG_PATH", openclawDir),
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

func resolveConfigPath(envKey, stateDir string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
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
	return l.Config + bakSuffix + "." + itoa(n)
}

// LastGoodPath returns the "<config>.last-good" sidecar path.
func (l Layer) LastGoodPath() string { return l.Config + lastGoodSuffix }

// LogsDir returns "<Dir>/logs".
func (l Layer) LogsDir() string { return filepath.Join(l.Dir, "logs") }

// ConfigAuditLogPath returns the JSONL audit-log path for this layer.
func (l Layer) ConfigAuditLogPath() string {
	return filepath.Join(l.LogsDir(), "config-audit.jsonl")
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

// CacheDir returns "<Dir>/cache".
func (l Layer) CacheDir() string { return filepath.Join(l.Dir, "cache") }

// SchemaCachePath returns the cached config schema path for this layer.
func (l Layer) SchemaCachePath() string {
	return filepath.Join(l.CacheDir(), "config-schema.json")
}

// itoa is a tiny helper to avoid importing strconv just for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
