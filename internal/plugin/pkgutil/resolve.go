// Package pkgutil holds helpers shared between the legacy and native
// plugin hosts. Both need to resolve a configured plugin cmd against
// the filesystem (with sibling-of-talon + PATH fallbacks) and both
// gate Host-service calls against the same method->capability map.
package pkgutil

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// ResolvePluginCmd locates the plugin binary referenced by cmd[0],
// falling back to sibling-of-talon then $PATH when the configured path
// isn't on disk. Returns the resolved cmd (cmd[0] possibly rewritten,
// cmd[1:] unchanged) or an error naming every location searched.
//
// Resolution order:
//
//  1. cmd[0] as-is (matches Docker / installed layouts at absolute paths).
//  2. Sibling of the talon binary (dev layout where bin/talon and bin/
//     plugin binaries land next to each other).
//  3. PATH lookup on basename (Homebrew-style installs).
//
// Each fallback hit is logged so a stale configured path is visible.
func ResolvePluginCmd(name string, cmd []string) ([]string, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("plugin %s: empty Cmd", name)
	}
	bin := cmd[0]
	if _, err := os.Stat(bin); err == nil {
		return cmd, nil
	}
	base := filepath.Base(bin)
	tried := []string{bin}
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), base)
		if sibling != bin {
			if _, err := os.Stat(sibling); err == nil {
				slog.Info("plugin cmd resolved via sibling",
					"plugin", name, "configured", bin, "resolved", sibling)
				out := append([]string{sibling}, cmd[1:]...)
				return out, nil
			}
			tried = append(tried, sibling)
		}
	}
	if found, err := exec.LookPath(base); err == nil && found != bin {
		slog.Info("plugin cmd resolved via PATH",
			"plugin", name, "configured", bin, "resolved", found)
		out := append([]string{found}, cmd[1:]...)
		return out, nil
	}
	tried = append(tried, "$PATH/"+base)
	return nil, fmt.Errorf("plugin %s: cmd not found (tried %v)", name, tried)
}
