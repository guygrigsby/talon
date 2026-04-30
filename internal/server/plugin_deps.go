package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
)

// PluginDepsHandler serves the plugins.deps.* RPCs the UI uses to drive
// runtime-dependency installation for bundled openclaw extensions.
//
// Bundled extensions ship with a package.json declaring runtime deps,
// but we don't pre-install them at vendor time (npm install per
// extension is cheap if the user's enabling 1-2, expensive if we
// blanket-install all 116). Manual install lets users opt in
// per-extension, with progress and errors surfaced through the UI
// rather than buried in a startup log line.
//
// Installs land in-place at <bundled.path>/<name>/node_modules/. In
// Docker that's ephemeral unless /opt/extensions is bind-mounted; the
// alternative (separate writable layout with NODE_PATH indirection)
// is a v1 concern.
type PluginDepsHandler struct {
	paths openclaw.Paths

	// npmCmd is the npm invocation. Replaceable so tests can stub it
	// without spawning real npm subprocesses. Default builds the
	// argv at call time so a test override fully short-circuits.
	npmCmd func(ctx context.Context, dir string) *exec.Cmd

	// installTimeout caps a single install. npm can hang on network
	// failures; we'd rather report a clear timeout than hold the WS
	// open indefinitely.
	installTimeout time.Duration
}

func NewPluginDepsHandler(paths openclaw.Paths) *PluginDepsHandler {
	h := &PluginDepsHandler{paths: paths, installTimeout: 5 * time.Minute}
	h.npmCmd = func(ctx context.Context, dir string) *exec.Cmd {
		c := exec.CommandContext(ctx, "npm", "install", "--omit=dev", "--no-audit", "--no-fund")
		c.Dir = dir
		return c
	}
	return h
}

// WithNpmCmd substitutes the npm-invocation builder; for tests.
func (h *PluginDepsHandler) WithNpmCmd(f func(ctx context.Context, dir string) *exec.Cmd) *PluginDepsHandler {
	h.npmCmd = f
	return h
}

func (h *PluginDepsHandler) Register(r *Registry) {
	r.Register("plugins.deps.status", h.handleStatus)
	r.Register("plugins.deps.install", h.handleInstall)
}

// --- plugins.deps.status ---------------------------------------------------

type pluginDepsStatusItem struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	HasPackageJSON    bool   `json:"hasPackageJson"`
	DepCount          int    `json:"depCount"`
	Installed         bool   `json:"installed"`
	NodeModulesExists bool   `json:"nodeModulesExists"`
	Error             string `json:"error,omitempty"`
}

func (h *PluginDepsHandler) handleStatus(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	root, ferr := h.bundledRoot()
	if ferr != nil {
		return nil, ferr
	}
	if root == "" {
		// No bundled path configured; return an empty list rather than
		// an error so the UI can render a "no extensions configured"
		// state without distinguishing it from a missing path.
		return map[string]any{"items": []pluginDepsStatusItem{}, "bundledPath": ""}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "plugins.deps.status: " + err.Error()}
	}
	items := make([]pluginDepsStatusItem, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip dotfiles + the npm-shared node_modules sibling at the
		// root so the list is just real extension dirs.
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		items = append(items, statusForExtension(filepath.Join(root, name), name))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return map[string]any{"items": items, "bundledPath": root}, nil
}

// statusForExtension reports whether an extension dir has a package.json
// with declared deps and whether node_modules is present. We treat
// "no deps declared" as installed=true so the UI doesn't nag the user
// to install nothing.
func statusForExtension(dir, name string) pluginDepsStatusItem {
	out := pluginDepsStatusItem{Name: name, Path: dir}
	pkgPath := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No package.json at all — extension has no runtime deps
			// (or isn't a real extension; both end up "nothing to do").
			out.Installed = true
			return out
		}
		out.Error = err.Error()
		return out
	}
	out.HasPackageJSON = true
	deps := gjson.GetBytes(raw, "dependencies")
	deps.ForEach(func(_, _ gjson.Result) bool {
		out.DepCount++
		return true
	})
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err == nil {
		out.NodeModulesExists = true
	}
	if out.DepCount == 0 {
		out.Installed = true
	} else {
		out.Installed = out.NodeModulesExists
	}
	return out
}

// --- plugins.deps.install --------------------------------------------------

type pluginDepsInstallParams struct {
	Name string `json:"name"`
}

func (h *PluginDepsHandler) handleInstall(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p pluginDepsInstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: name is required"}
	}
	// Reject anything that isn't a bare directory name — no slashes,
	// no traversal. Defense-in-depth even though the UI only ever
	// sends names from plugins.deps.status's own output.
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: invalid name"}
	}
	root, ferr := h.bundledRoot()
	if ferr != nil {
		return nil, ferr
	}
	if root == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: plugins.bundled.path not configured"}
	}
	dir := filepath.Join(root, p.Name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: extension not found: " + p.Name}
	}
	pkgPath := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		// Nothing to install — return ok with a no-op note.
		return map[string]any{
			"ok":      true,
			"name":    p.Name,
			"skipped": "no package.json",
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.installTimeout)
	defer cancel()
	cmd := h.npmCmd(ctx, dir)
	out, err := cmd.CombinedOutput()
	tail := tailLines(string(out), 80) // bound the response payload
	if err != nil {
		return map[string]any{
			"ok":     false,
			"name":   p.Name,
			"error":  err.Error(),
			"output": tail,
		}, nil
	}
	// Re-stat to confirm node_modules exists (npm sometimes reports
	// success without writing anything if package.json had no deps
	// but a leftover lockfile, etc.).
	status := statusForExtension(dir, p.Name)
	return map[string]any{
		"ok":     true,
		"name":   p.Name,
		"status": status,
		"output": tail,
	}, nil
}

// bundledRoot resolves plugins.bundled.path the same way the spawn
// path does — config first, env var second, /opt/extensions third.
// Kept separate from the cmd/talon-side defaults for testability;
// the in-server handler sees only the merged config + env.
func (h *PluginDepsHandler) bundledRoot() (string, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return "", &FrameError{Code: ErrCodeInternal, Message: "plugins.deps: read config: " + err.Error()}
	}
	if v := gjson.GetBytes(merged, "plugins.bundled.path"); v.Exists() && v.Str != "" {
		return v.Str, nil
	}
	if v := os.Getenv("TALON_EXTENSIONS_PATH"); v != "" {
		return v, nil
	}
	if _, err := os.Stat("/opt/extensions"); err == nil {
		return "/opt/extensions", nil
	}
	return "", nil
}

// tailLines returns at most n lines from the tail of s. Keeps the UI
// payload small while preserving the tail of npm's output (which is
// where "Wrote N packages in 12s" or the actual error tends to land).
func tailLines(s string, n int) string {
	if s == "" || n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// guard against unused import in stripped-down builds
var _ = fmt.Sprintf
