// Package pkgutil holds helpers shared by the plugin host surfaces:
// configured command resolution and Host-service method capability mapping.
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
//  2. TALON_PLUGIN_PATH entries, then ~/.talon/plugins.
//  3. Sibling of the talon binary (dev layout where bin/talon and bin/
//     plugin binaries land next to each other).
//  4. PATH lookup on basename (Homebrew-style installs).
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
	for _, dir := range pluginSearchDirs() {
		candidate := filepath.Join(dir, base)
		if candidate == bin {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			slog.Info("plugin cmd resolved via plugin dir",
				"plugin", name, "configured", bin, "resolved", candidate)
			out := append([]string{candidate}, cmd[1:]...)
			return out, nil
		}
		tried = append(tried, candidate)
	}
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

func pluginSearchDirs() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	for _, dir := range filepath.SplitList(os.Getenv("TALON_PLUGIN_PATH")) {
		add(dir)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		add(filepath.Join(home, ".talon", "plugins"))
	}
	return out
}
