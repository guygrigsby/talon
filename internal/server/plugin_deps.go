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
	plugin "github.com/guygrigsby/talon/internal/plugin/host"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// PluginDepsHandler serves the plugins.deps.* RPCs the UI uses to inspect
// native Talon plugins and optional third-party plugin directories.
type PluginDepsHandler struct {
	paths talonpath.Paths

	// host, when non-nil, lets the handler report whether each
	// plugin is currently loaded by the gateway's plugin runtime.
	// Set via WithHost from cmd/talon (same plugin.Host that owns
	// every spawned subprocess plugin).
	host *plugin.Host

	// npmCmd is the npm invocation. Replaceable so tests can stub it
	// without spawning real npm subprocesses. Default builds the
	// argv at call time so a test override fully short-circuits.
	npmCmd func(ctx context.Context, dir string) *exec.Cmd

	// installTimeout caps a single install. npm can hang on network
	// failures; we'd rather report a clear timeout than hold the WS
	// open indefinitely.
	installTimeout time.Duration
}

// WithHost wires the plugin host the gateway uses to track loaded
// subprocess plugins. Required for the "Loaded" status surface in
// the /plugins UI; safe to leave nil (plugins still appear, just
// without the loaded marker).
func (h *PluginDepsHandler) WithHost(host *plugin.Host) *PluginDepsHandler {
	h.host = host
	return h
}

func NewPluginDepsHandler(paths talonpath.Paths) *PluginDepsHandler {
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
	r.Register("plugins.deps.uninstall", h.handleUninstall)
	r.Register("plugins.deps.detail", h.handleDetail)
}

// --- plugins.deps.uninstall ------------------------------------------------

type pluginDepsUninstallParams struct {
	Name string `json:"name"`
}

// handleUninstall removes a third-party plugin directory from Talon state.
// Built-in plugins are part of the talon binary and are not uninstallable
// through this RPC.
func (h *PluginDepsHandler) handleUninstall(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p pluginDepsUninstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.uninstall: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.uninstall: name is required"}
	}
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.uninstall: invalid name"}
	}
	talonRoot := h.talonExtensionsRoot()
	if talonRoot == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.uninstall: no writable talon overlay configured"}
	}
	target := filepath.Join(talonRoot, p.Name)
	if _, err := os.Stat(target); err != nil {
		// Not in the overlay — nothing for us to remove. Surface
		// this as ok with a no-op note rather than an RPC error so
		// the UI can render an informative message inline.
		return map[string]any{
			"ok":      true,
			"name":    p.Name,
			"skipped": "not present in talon overlay",
		}, nil
	}
	if err := os.RemoveAll(target); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "plugins.deps.uninstall: " + err.Error()}
	}
	// Re-stat to report the post-uninstall state so the UI swaps the row
	// in place.
	dir, label := h.locateExtension(p.Name)
	if dir == "" {
		return map[string]any{
			"ok":   true,
			"name": p.Name,
			// No status block: the plugin is gone.
		}, nil
	}
	status := statusForExtension(dir, p.Name)
	status.Source = label
	merged, _ := config.MergedBytes(h.paths)
	enrichInUse(merged, &status)
	enrichUninstallable(talonRoot, &status)
	return map[string]any{
		"ok":     true,
		"name":   p.Name,
		"status": status,
	}, nil
}

// enrichInUse sets InUse=true when the merged config references this plugin.
// Cheap to compute; the UI uses it to flag live integrations before removal.
func enrichInUse(merged []byte, item *pluginDepsStatusItem) {
	if v := gjson.GetBytes(merged, "plugins.entries."+item.Name+".enabled"); v.Bool() {
		item.InUse = true
		return
	}
	if item.Source == "builtin" && item.Kind == "channel" {
		if v := gjson.GetBytes(merged, "channels."+item.Name); v.Exists() {
			item.InUse = true
		}
		return
	}
	// Channel-binding signal for third-party metadata.
	if item.Path == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(item.Path, "package.json"))
	if err != nil {
		return
	}
	channelID := gjson.GetBytes(raw, "talon.channel.id").Str
	if channelID == "" {
		return
	}
	if v := gjson.GetBytes(merged, "channels."+channelID); v.Exists() {
		item.InUse = true
	}
}

// enrichUninstallable marks the row as uninstallable when its on-disk path
// lives under Talon's plugin directory.
func enrichUninstallable(talonRoot string, item *pluginDepsStatusItem) {
	if talonRoot == "" || item.Path == "" {
		return
	}
	clean := filepath.Clean(talonRoot)
	itemPath := filepath.Clean(item.Path)
	rel, err := filepath.Rel(clean, itemPath)
	if err != nil {
		return
	}
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		item.Uninstallable = true
	}
}

// --- plugins.deps.detail ---------------------------------------------------

type pluginDepsDetailParams struct {
	Name string `json:"name"`
}

type pluginDepsDetail struct {
	pluginDepsStatusItem
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Blurb        string            `json:"blurb,omitempty"`
	DocsPath     string            `json:"docsPath,omitempty"`
	ChannelID    string            `json:"channelId,omitempty"`
	PackageName  string            `json:"packageName,omitempty"`
}

// handleDetail returns the rich per-plugin payload the UI's drill-down
// panel wants: dependency map, channel/provider blurb, docs link, and
// package name. Status-shape fields are embedded so a detail call alone
// is enough to render.
func (h *PluginDepsHandler) handleDetail(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p pluginDepsDetailParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.detail: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.detail: name is required"}
	}
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.detail: invalid name"}
	}

	// Native plugins win over any same-named third-party directory.
	for _, b := range builtinPlugins {
		if b.EntryName == p.Name {
			out := pluginDepsDetail{pluginDepsStatusItem: pluginDepsStatusItem{
				Name:        b.EntryName,
				Path:        b.Command,
				Source:      "builtin",
				Description: b.Description,
				Version:     b.Version,
				Kind:        b.Kind,
				Label:       b.Label,
				Installed:   true,
			}}
			merged, _ := config.MergedBytes(h.paths)
			enrichInUse(merged, &out.pluginDepsStatusItem)
			if h.host != nil {
				for _, name := range h.host.List() {
					if name == b.EntryName {
						out.Loaded = true
						break
					}
				}
			}
			return out, nil
		}
	}

	dir, label := h.locateExtension(p.Name)
	if dir == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.detail: plugin not found: " + p.Name}
	}
	out := pluginDepsDetail{pluginDepsStatusItem: statusForExtension(dir, p.Name)}
	out.Source = label
	merged, _ := config.MergedBytes(h.paths)
	enrichInUse(merged, &out.pluginDepsStatusItem)
	enrichUninstallable(h.talonExtensionsRoot(), &out.pluginDepsStatusItem)

	pkgPath := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		// status already covers package.json absence; pass through.
		return out, nil
	}
	out.PackageName = gjson.GetBytes(raw, "name").Str
	deps := map[string]string{}
	gjson.GetBytes(raw, "dependencies").ForEach(func(k, v gjson.Result) bool {
		if k.Str != "" {
			deps[k.Str] = v.Str
		}
		return true
	})
	if len(deps) > 0 {
		out.Dependencies = deps
	}
	// Channel-side metadata: blurb, docsPath, id (used by config).
	if v := gjson.GetBytes(raw, "talon.channel.blurb"); v.Exists() && v.Str != "" {
		out.Blurb = v.Str
	}
	if v := gjson.GetBytes(raw, "talon.channel.docsPath"); v.Exists() && v.Str != "" {
		out.DocsPath = v.Str
	}
	if v := gjson.GetBytes(raw, "talon.channel.id"); v.Exists() && v.Str != "" {
		out.ChannelID = v.Str
	}
	return out, nil
}

// --- plugins.deps.status ---------------------------------------------------

type pluginDepsStatusItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Source identifies where this plugin came from: "builtin" or
	// "talon". The UI surfaces it so users can distinguish shipped
	// plugins from third-party local entries.
	Source string `json:"source"`
	// Description / Version are populated from package metadata when a
	// third-party plugin directory has it, or from builtin metadata.
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Kind classifies the plugin surface: "channel", "provider", or
	// "plugin". Useful for filtering and badges in the UI.
	Kind string `json:"kind,omitempty"`
	// Label is the user-facing display name. Falls back to "" so the UI
	// shows Name in that case.
	Label             string `json:"label,omitempty"`
	HasPackageJSON    bool   `json:"hasPackageJson"`
	DepCount          int    `json:"depCount"`
	Installed         bool   `json:"installed"`
	NodeModulesExists bool   `json:"nodeModulesExists"`
	// InUse=true when the merged config references this plugin. The UI
	// surfaces a badge so users know uninstalling will break a live
	// integration.
	InUse bool `json:"inUse"`
	// Uninstallable=true when the plugin exists in Talon's writable
	// plugin directory. Builtins are not uninstallable.
	Uninstallable bool `json:"uninstallable"`
	// Loaded=true when the gateway's plugin runtime currently has
	// this plugin as a live subprocess. UI surfaces it as a
	// "Loaded" badge so users can tell active integrations from
	// merely-available ones.
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
}

// builtinPlugin describes a Go-implemented plugin shipped in the talon
// binary. The UI shows these as ordinary plugins.
type builtinPlugin struct {
	// EntryName is the canonical name used as the
	// plugins.entries.<name> key when the user enables this plugin.
	EntryName string
	// Command is the human-readable spawn target.
	Command string
	// Manifest fields surfaced in the listing without spawning the
	// binary first. Once loaded, the live manifest takes over.
	Description string
	Version     string
	Kind        string // "channel" | "provider" | "plugin"
	Label       string // user-facing display name
}

// builtinPlugins is the registry of bundled Go plugins.
// Single source of truth for the /plugins UI's pre-spawn metadata.
var builtinPlugins = []builtinPlugin{
	{
		EntryName:   "anthropic",
		Command:     "talon plugin run anthropic",
		Description: "Anthropic Messages API provider (Claude)",
		Version:     "0.1.0",
		Kind:        "provider",
		Label:       "Anthropic",
	},
	{
		EntryName:   "openai-compat",
		Command:     "talon plugin run openai-compat",
		Description: "OpenAI-compatible providers (multi-tenant): openai, deepseek, mistral, mlx, lmstudio, ollama",
		Version:     "0.1.0",
		Kind:        "provider",
		Label:       "OpenAI-Compatible",
	},
	{
		EntryName:   "telegram",
		Command:     "talon plugin run telegram",
		Description: "Telegram bot channel: long-poll + sendMessage",
		Version:     "0.1.0",
		Kind:        "channel",
		Label:       "Telegram",
	},
	{
		EntryName:   "brave",
		Command:     "talon plugin run brave",
		Description: "Brave Search web_search tool",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Brave Search",
	},
	{
		EntryName:   "whisper",
		Command:     "talon plugin run whisper",
		Description: "OpenAI Whisper transcription tool",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Whisper Transcription",
	},
	{
		EntryName:   "bluebubbles",
		Command:     "talon plugin run bluebubbles",
		Description: "BlueBubbles iMessage channel: webhook in, REST out",
		Version:     "0.1.0",
		Kind:        "channel",
		Label:       "BlueBubbles",
	},
	{
		EntryName:   "mac-notify",
		Command:     "talon plugin run mac-notify",
		Description: "Local macOS Notification Center via osascript (mac_notify tool)",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Mac Notify",
	},
	{
		EntryName:   "mac-open",
		Command:     "talon plugin run mac-open",
		Description: "Launch macOS apps and open URLs/files in specific apps via the `open` command (mac_open tool)",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Mac Open",
	},
}

// BuiltinPluginCmd returns the default spawn cmd for a first-party
// plugin name, or nil if the name isn't bundled. Lets callers (chiefly
// the gateway's plugin-spec parser) fall through to a sane default
// when an enabled entry has no explicit cmd in config.
//
// The command is self-referential: [<talon-binary>, "plugin", "run",
// name]. No separate plugin binary is required — the running talon
// binary handles all bundled plugins via 'talon plugin run <name>'.
// If os.Executable fails, "talon" is used as a PATH fallback.
func BuiltinPluginCmd(name string) []string {
	for _, b := range builtinPlugins {
		if b.EntryName == name {
			exe, err := os.Executable()
			if err != nil {
				exe = "talon"
			}
			return []string{exe, "plugin", "run", name}
		}
	}
	return nil
}

// BuiltinPluginNames returns every first-party plugin name in the
// order they're registered. Used by the gateway plugin loader to
// auto-enable bundled plugins that aren't explicitly listed in
// plugins.entries — so a user who never touches plugin config
// still gets mac-notify, mac-open, telegram, etc.
func BuiltinPluginNames() []string {
	out := make([]string, 0, len(builtinPlugins))
	for _, b := range builtinPlugins {
		out = append(out, b.EntryName)
	}
	return out
}

// extensionSource pairs a directory with a label for the lookup-chain
// merge. Order matters: earlier entries win on name collision.
type extensionSource struct {
	root  string
	label string
}

func (h *PluginDepsHandler) handleStatus(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	sources := h.extensionSources()
	if len(sources) == 0 {
		return map[string]any{
			"items":     []pluginDepsStatusItem{},
			"sources":   []map[string]string{},
			"writeRoot": h.talonExtensionsRoot(),
		}, nil
	}

	merged, _ := config.MergedBytes(h.paths)
	talonRoot := h.talonExtensionsRoot()
	loadedNames := h.loadedPluginNames()
	seen := map[string]bool{}
	items := make([]pluginDepsStatusItem, 0)

	// Built-in plugins (Go binaries shipped with talon) appear first
	// in the merged list so the UI sees them on top. They have no
	// package.json / node_modules state — installation is just
	// "enabled in plugins.entries.<name>".
	for _, b := range builtinPlugins {
		seen[b.EntryName] = true
		item := pluginDepsStatusItem{
			Name:        b.EntryName,
			Path:        b.Command,
			Source:      "builtin",
			Description: b.Description,
			Version:     b.Version,
			Kind:        b.Kind,
			Label:       b.Label,
			Installed:   true,
		}
		enrichInUse(merged, &item)
		if loadedNames[b.EntryName] {
			item.Loaded = true
		}
		items = append(items, item)
	}

	for _, src := range sources {
		entries, err := os.ReadDir(src.root)
		if err != nil {
			// Missing dirs in the chain are fine — the chain itself
			// is what falls through to fallbacks. Real I/O errors on
			// existing dirs we surface so users notice perms issues.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, &FrameError{Code: ErrCodeInternal, Message: "plugins.deps.status: " + err.Error()}
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				continue
			}
			if seen[name] {
				continue // earlier source already provided this name
			}
			seen[name] = true
			item := statusForExtension(filepath.Join(src.root, name), name)
			item.Source = src.label
			enrichInUse(merged, &item)
			enrichUninstallable(talonRoot, &item)
			if loadedNames[name] {
				item.Loaded = true
			}
			items = append(items, item)
		}
	}
	// Native plugins come first, then alphabetical within each
	// group. Users opened with the builtin set on top because that's
	// the always-loaded surface; demoting them under "deepseek..."
	// alphabetic position is what people noticed.
	sort.SliceStable(items, func(i, j int) bool {
		ni, nj := items[i].Source == "builtin", items[j].Source == "builtin"
		if ni != nj {
			return ni
		}
		return items[i].Name < items[j].Name
	})
	srcRows := make([]map[string]string, 0, len(sources))
	for _, s := range sources {
		srcRows = append(srcRows, map[string]string{"label": s.label, "path": s.root})
	}
	return map[string]any{
		"items":     items,
		"sources":   srcRows,
		"writeRoot": h.talonExtensionsRoot(),
	}, nil
}

// statusForExtension reports whether an extension dir has a package.json
// with declared deps and whether node_modules is present. We treat
// "no deps declared" as installed=true so the UI doesn't nag the user
// to install nothing.
//
// Note: inUse + uninstallable are NOT computed here — the function's
// signature stays terse for the many call sites that only need
// installation state. handleStatus/handleDetail compute those after
// statusForExtension returns, since they require the merged config
// and the talon-root path.
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
	out.Description = gjson.GetBytes(raw, "description").Str
	out.Version = gjson.GetBytes(raw, "version").Str
	// Classify by the richest Talon metadata available. Channel wins
	// over provider when both are present.
	if v := gjson.GetBytes(raw, "talon.channel.label"); v.Exists() && v.Str != "" {
		out.Kind = "channel"
		out.Label = v.Str
	} else if v := gjson.GetBytes(raw, "talon.provider.label"); v.Exists() && v.Str != "" {
		out.Kind = "provider"
		out.Label = v.Str
	} else if v := gjson.GetBytes(raw, "talon.channel"); v.Exists() {
		if v.Type == gjson.String && v.Str != "" {
			out.Kind = "channel"
			out.Label = v.Str
		}
	} else if v := gjson.GetBytes(raw, "talon.provider"); v.Exists() {
		if v.Type == gjson.String && v.Str != "" {
			out.Kind = "provider"
			out.Label = v.Str
		}
	}
	if out.Kind == "" {
		out.Kind = "plugin"
	}
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
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: invalid name"}
	}

	// Resolve the source dir from the lookup chain. The result
	// becomes the install destination too — except when the source
	// is a read-only fallback, in which case we copy it into the
	// talon overlay first.
	srcDir, srcLabel := h.locateExtension(p.Name)
	if srcDir == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: plugin not found: " + p.Name}
	}
	if _, err := os.Stat(filepath.Join(srcDir, "package.json")); err != nil {
		return map[string]any{
			"ok":      true,
			"name":    p.Name,
			"skipped": "no package.json",
		}, nil
	}

	dir := srcDir
	talonRoot := h.talonExtensionsRoot()
	if srcLabel != "talon" && talonRoot != "" {
		dest := filepath.Join(talonRoot, p.Name)
		if err := copyDirectory(srcDir, dest); err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: "plugins.deps.install: copy to overlay: " + err.Error()}
		}
		dir = dest
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.installTimeout)
	defer cancel()
	cmd := h.npmCmd(ctx, dir)
	out, err := cmd.CombinedOutput()
	tail := tailLines(string(out), 80)
	if err != nil {
		return map[string]any{
			"ok":     false,
			"name":   p.Name,
			"error":  err.Error(),
			"output": tail,
		}, nil
	}
	status := statusForExtension(dir, p.Name)
	if dir == filepath.Join(talonRoot, p.Name) {
		status.Source = "talon"
	} else {
		status.Source = srcLabel
	}
	merged, _ := config.MergedBytes(h.paths)
	enrichInUse(merged, &status)
	enrichUninstallable(talonRoot, &status)
	return map[string]any{
		"ok":     true,
		"name":   p.Name,
		"status": status,
		"output": tail,
	}, nil
}

// loadedPluginNames returns the set of plugin names the gateway's
// runtime currently has spawned. Built from host.List(); empty when
// the host wasn't wired in (tests, paths.Talon.Dir == "").
func (h *PluginDepsHandler) loadedPluginNames() map[string]bool {
	if h.host == nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	for _, name := range h.host.List() {
		out[name] = true
	}
	return out
}

// extensionSources returns optional third-party plugin metadata directories.
// Native builtin plugins are listed separately.
func (h *PluginDepsHandler) extensionSources() []extensionSource {
	out := []extensionSource{}
	if root := h.talonExtensionsRoot(); root != "" {
		out = append(out, extensionSource{root: root, label: "talon"})
	}
	return out
}

// locateExtension walks the chain and returns the first dir + label
// that contains a directory matching name. Returns ("", "") when no
// source has it.
func (h *PluginDepsHandler) locateExtension(name string) (dir, label string) {
	for _, src := range h.extensionSources() {
		candidate := filepath.Join(src.root, name)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate, src.label
		}
	}
	return "", ""
}

// talonExtensionsRoot is the writable destination for third-party plugins.
// Defaults to <Talon.Dir>/plugins; overridable via plugins.load_paths[0].
func (h *PluginDepsHandler) talonExtensionsRoot() string {
	if h.paths.Talon.Dir == "" {
		return ""
	}
	merged, err := config.MergedBytes(h.paths)
	if err == nil {
		if v := gjson.GetBytes(merged, "plugins.load.paths.0"); v.Exists() && v.Str != "" {
			return v.Str
		}
	}
	return h.paths.Talon.PluginsDir()
}

// copyDirectory clones src into dst recursively.
func copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		// Skip symlinks and special files.
		if info.Mode()&os.ModeType != 0 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
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
