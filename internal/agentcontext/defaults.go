package agentcontext

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// defaultTemplates holds the seed persona files written into a fresh
// workspace. They carry talon's operator-channel trust framing so new
// installs don't inherit the old openclaw "anything that leaves the
// machine = exfiltration" rule.
//
//go:embed templates/AGENTS.md templates/SOUL.md templates/IDENTITY.md templates/USER.md
var defaultTemplates embed.FS

// EnsureDefaults seeds any missing persona file in dir with its default
// template. Existing files are never overwritten — a user's customized
// IDENTITY/SOUL/etc. is left exactly as-is. Returns the names of the
// files it created (nil when dir is empty or everything already
// existed), so the caller can log a one-time "scaffolded" line.
//
// Idempotent: safe to call on every startup. Write-if-missing only,
// so it's not a config mutation and needs no reload signal.
func EnsureDefaults(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace %s: %w", dir, err)
	}

	var created []string
	for _, name := range canonicalOrder {
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // already present — never clobber
		} else if !os.IsNotExist(err) {
			return created, fmt.Errorf("stat %s: %w", dst, err)
		}
		body, err := defaultTemplates.ReadFile("templates/" + name)
		if err != nil {
			// No embedded template for this canonical file (e.g. a
			// future addition without a seed). Skip rather than fail.
			continue
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return created, fmt.Errorf("write %s: %w", dst, err)
		}
		created = append(created, name)
	}
	return created, nil
}
