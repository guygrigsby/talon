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
	plugin "github.com/guygrigsby/talon/internal/plugin/legacy"
	"github.com/tidwall/gjson"
)

// PluginDepsHandler serves the plugins.deps.* RPCs the UI uses to drive
// runtime-dependency installation for bundled openclaw extensions.
//
// Lookup chain for extension code (highest priority first):
//
//  1. ~/.talon/extensions/<name>      — talon overlay (writable)
//  2. ~/.openclaw/extensions/<name>   — openclaw layer (read-only,
//                                        picks up user installs from
//                                        a prior openclaw setup)
//  3. /opt/extensions/<name>          — image-baked bundle (Docker)
//
// Install destination is always (1). If the source for a given
// extension is (2) or (3), the install path copies it into the talon
// overlay first, then npm install runs in the writable copy. This
// gives drop-in compat with openclaw's user-install location AND
// makes installs survive container rebuilds without a separate
// volume mount (~/.talon is already a host bind in docker-run).
type PluginDepsHandler struct {
	paths openclaw.Paths

	// host, when non-nil, lets the handler report whether each
	// extension is currently loaded by the gateway's plugin runtime.
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
	r.Register("plugins.deps.uninstall", h.handleUninstall)
	r.Register("plugins.deps.detail", h.handleDetail)
}

// --- plugins.deps.uninstall ------------------------------------------------

type pluginDepsUninstallParams struct {
	Name string `json:"name"`
}

// handleUninstall removes the extension's talon-overlay copy. The
// bundle (or openclaw layer) stays intact, so a subsequent install
// or status call falls back through the lookup chain. We refuse to
// touch anything outside the talon overlay — that's the only
// writable layer in talon's contract.
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
	// After uninstall, the lookup chain may resurface a bundled or
	// openclaw-layer copy; re-stat to report the post-uninstall
	// state so the UI swaps the row in place.
	dir, label := h.locateExtension(p.Name)
	if dir == "" {
		return map[string]any{
			"ok":   true,
			"name": p.Name,
			// No status block — the extension is gone from every layer.
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

// enrichInUse sets InUse=true when the merged config references this
// extension through any of three signals:
//   - plugins.entries.<name>.enabled: explicit per-name entry on
//   - plugins.entries.*.bundled == name: indirect reference by entry
//   - channels.<channelId> exists when the extension declares
//     openclaw.channel.id == channelId: live channel binding
//
// Cheap to compute; the UI uses it to surface a "Required (in use)"
// badge so uninstalling a live integration is at least flagged.
func enrichInUse(merged []byte, item *pluginDepsStatusItem) {
	if v := gjson.GetBytes(merged, "plugins.entries."+item.Name+".enabled"); v.Bool() {
		item.InUse = true
		return
	}
	gjson.GetBytes(merged, "plugins.entries").ForEach(func(_, entry gjson.Result) bool {
		if entry.Get("bundled").Str == item.Name && entry.Get("enabled").Bool() {
			item.InUse = true
			return false
		}
		return true
	})
	if item.InUse {
		return
	}
	// Builtin channel plugins: name matches the channel id directly,
	// no package.json to consult. Mirrors the openclaw-extension
	// path below but skips the file read.
	if item.Source == "builtin" && item.Kind == "channel" {
		if v := gjson.GetBytes(merged, "channels."+item.Name); v.Exists() {
			item.InUse = true
		}
		return
	}
	// Channel-binding signal: load the package.json's channel.id
	// and check if it's keyed under channels.*.
	if item.Path == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(item.Path, "package.json"))
	if err != nil {
		return
	}
	channelID := gjson.GetBytes(raw, "openclaw.channel.id").Str
	if channelID == "" {
		return
	}
	if v := gjson.GetBytes(merged, "channels."+channelID); v.Exists() {
		item.InUse = true
	}
}

// enrichUninstallable marks the row as uninstallable when its on-disk
// path lives under the talon overlay (the only layer talon will
// write to). Bundled/openclaw entries stay false; the UI omits the
// uninstall button for those.
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

// handleDetail returns the rich per-extension payload the UI's
// drill-down panel wants: full dependency map (name → semver),
// channel/provider blurb, docs link, and the underlying npm package
// name. Status-shape fields are embedded so a detail call alone is
// enough to render — the UI doesn't have to combine two responses.
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

	// Native plugins win over any same-named bundled extension.
	// Without this check, plugins.deps.detail would fall through to
	// the openclaw extension dir lookup and surface the WRONG
	// metadata (npm package, channel blurb, full TS-side dep list)
	// for a name the user thinks of as the native binary.
	for _, b := range builtinPlugins {
		if b.EntryName == p.Name {
			out := pluginDepsDetail{pluginDepsStatusItem: pluginDepsStatusItem{
				Name:        b.EntryName,
				Path:        b.BinaryPath,
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
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.detail: extension not found: " + p.Name}
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
	if v := gjson.GetBytes(raw, "openclaw.channel.blurb"); v.Exists() && v.Str != "" {
		out.Blurb = v.Str
	}
	if v := gjson.GetBytes(raw, "openclaw.channel.docsPath"); v.Exists() && v.Str != "" {
		out.DocsPath = v.Str
	}
	if v := gjson.GetBytes(raw, "openclaw.channel.id"); v.Exists() && v.Str != "" {
		out.ChannelID = v.Str
	}
	return out, nil
}

// --- plugins.deps.status ---------------------------------------------------

type pluginDepsStatusItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Source identifies which dir in the lookup chain this extension
	// came from: "talon" / "openclaw" / "bundled". The UI surfaces
	// it so users can see whether they're looking at their own
	// installs or the shipped defaults.
	Source string `json:"source"`
	// Description / Version are the package.json fields openclaw uses
	// in its bundle metadata. Populated whenever a package.json exists.
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Kind classifies the extension surface from the openclaw block:
	// "channel" / "provider" / "plugin". Useful for filtering and
	// for badges in the UI.
	Kind string `json:"kind,omitempty"`
	// Label is the user-facing display name for channel/provider
	// extensions (openclaw.channel.label or openclaw.provider.label).
	// Falls back to "" — UI shows Name in that case.
	Label             string `json:"label,omitempty"`
	HasPackageJSON    bool   `json:"hasPackageJson"`
	DepCount          int    `json:"depCount"`
	Installed         bool   `json:"installed"`
	NodeModulesExists bool   `json:"nodeModulesExists"`
	// InUse=true when the merged config references this extension —
	// either via plugins.entries.<name>.enabled or as a configured
	// channel keyed by openclaw.channel.id. The UI surfaces a
	// "Required" / "In use" badge for these so users know
	// uninstalling will break a live integration.
	InUse bool `json:"inUse"`
	// Uninstallable=true when the extension exists in the talon
	// overlay (the only writable layer); otherwise removing it would
	// require touching ~/.openclaw or the image bundle, both of
	// which talon refuses to write.
	Uninstallable bool `json:"uninstallable"`
	// Loaded=true when the gateway's plugin runtime currently has
	// this extension as a live subprocess. UI surfaces it as a
	// "Loaded" badge so users can tell active integrations from
	// merely-available ones.
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
}

// builtinPlugin describes a Go-implemented plugin shipped in the
// talon binary tree (binary already on PATH after install). They
// surface in the same plugins list as the openclaw-bundled
// extensions — users don't need to care about implementation
// language; the UI shows them as ordinary plugins.
type builtinPlugin struct {
	// EntryName is the canonical name used as the
	// plugins.entries.<name> key when the user enables this plugin.
	EntryName string
	// BinaryPath is the spawn target — wired into the cmd array
	// when the user enables the plugin.
	BinaryPath string
	// Manifest fields surfaced in the listing without spawning the
	// binary first. Once loaded, the live manifest takes over.
	Description string
	Version     string
	Kind        string // "channel" | "provider" | "plugin"
	Label       string // user-facing display name
}

// builtinPlugins is the registry of bundled Go plugins. New entries
// land here when their binary is added to the Dockerfile build.
// Single source of truth for the /plugins UI's pre-spawn metadata.
var builtinPlugins = []builtinPlugin{
	{
		EntryName:   "deepseek",
		BinaryPath:  "/usr/local/bin/talon-deepseek-plugin",
		Description: "DeepSeek chat-completions provider",
		Version:     "0.1.0",
		Kind:        "provider",
		Label:       "DeepSeek",
	},
	{
		EntryName:   "telegram",
		BinaryPath:  "/usr/local/bin/talon-telegram-plugin",
		Description: "Telegram bot channel — long-poll + sendMessage",
		Version:     "0.1.0",
		Kind:        "channel",
		Label:       "Telegram",
	},
	{
		EntryName:   "brave",
		BinaryPath:  "/usr/local/bin/talon-brave-plugin",
		Description: "Brave Search web_search tool (replaces openclaw extensions/brave)",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Brave Search",
	},
	{
		EntryName:   "whisper",
		BinaryPath:  "/usr/local/bin/talon-whisper-plugin",
		Description: "OpenAI Whisper transcription tool (replaces openclaw skills/openai-whisper-api)",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Whisper Transcription",
	},
	{
		EntryName:   "bluebubbles",
		BinaryPath:  "/usr/local/bin/talon-bluebubbles-plugin",
		Description: "BlueBubbles iMessage channel — webhook in, REST out (replaces openclaw extensions/bluebubbles)",
		Version:     "0.1.0",
		Kind:        "channel",
		Label:       "BlueBubbles",
	},
	{
		EntryName:   "mac-notify",
		BinaryPath:  "/usr/local/bin/talon-mac-notify-plugin",
		Description: "Local macOS Notification Center via osascript (mac_notify tool)",
		Version:     "0.1.0",
		Kind:        "plugin",
		Label:       "Mac Notify",
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
			"items":      []pluginDepsStatusItem{},
			"sources":    []map[string]string{},
			"writeRoot":  h.talonExtensionsRoot(),
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
			Path:        b.BinaryPath,
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
	// Classify by the richest openclaw metadata available. channel
	// wins over provider when both are present (rare in practice;
	// channel is the load-bearing role).
	if v := gjson.GetBytes(raw, "openclaw.channel.label"); v.Exists() && v.Str != "" {
		out.Kind = "channel"
		out.Label = v.Str
	} else if v := gjson.GetBytes(raw, "openclaw.provider.label"); v.Exists() && v.Str != "" {
		out.Kind = "provider"
		out.Label = v.Str
	} else if v := gjson.GetBytes(raw, "openclaw.channel"); v.Exists() {
		// Some packages ship a stripped openclaw block where channel
		// is just a bare label string ("Slack"). Detect that shape.
		if v.Type == gjson.String && v.Str != "" {
			out.Kind = "channel"
			out.Label = v.Str
		}
	} else if v := gjson.GetBytes(raw, "openclaw.provider"); v.Exists() {
		if v.Type == gjson.String && v.Str != "" {
			out.Kind = "provider"
			out.Label = v.Str
		}
	}
	if out.Kind == "" {
		// Default to "plugin" — generic role. Tracker still fires
		// captured-but-ignored register* warnings via the shim, so
		// "plugin" doesn't promise functionality, just packaging.
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
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "plugins.deps.install: extension not found: " + p.Name}
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

// extensionSources returns the lookup chain talon walks to find
// extension dirs. Order matters — the talon overlay wins, then the
// openclaw layer (read-only, drop-in compat), then the image-baked
// bundle. Absent dirs aren't filtered here; callers handle ENOENT.
func (h *PluginDepsHandler) extensionSources() []extensionSource {
	out := []extensionSource{}
	if root := h.talonExtensionsRoot(); root != "" {
		out = append(out, extensionSource{root: root, label: "talon"})
	}
	if root := h.openclawExtensionsRoot(); root != "" {
		out = append(out, extensionSource{root: root, label: "openclaw"})
	}
	if root := h.bundledExtensionsRoot(); root != "" {
		// Avoid double-listing if a config explicitly pointed
		// bundled.paths at one of the layered dirs.
		duplicate := false
		for _, s := range out {
			if s.root == root {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, extensionSource{root: root, label: "bundled"})
		}
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

// talonExtensionsRoot is the writable destination for installs.
// Defaults to <Talon.Dir>/extensions; overridable via
// plugins.bundled.writeRoot in config (rarely needed).
func (h *PluginDepsHandler) talonExtensionsRoot() string {
	if h.paths.Talon.Dir == "" {
		return ""
	}
	merged, err := config.MergedBytes(h.paths)
	if err == nil {
		if v := gjson.GetBytes(merged, "plugins.bundled.writeRoot"); v.Exists() && v.Str != "" {
			return v.Str
		}
	}
	return filepath.Join(h.paths.Talon.Dir, "extensions")
}

// openclawExtensionsRoot returns ~/.openclaw/extensions when present.
// We don't WRITE there (that violates talon's read-only invariant on
// the openclaw layer) but reading lets users with a prior openclaw
// install see their custom extensions in talon's UI without copying.
func (h *PluginDepsHandler) openclawExtensionsRoot() string {
	if h.paths.Openclaw.Dir == "" || h.paths.SkipOpenclaw {
		return ""
	}
	candidate := filepath.Join(h.paths.Openclaw.Dir, "extensions")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// bundledExtensionsRoot returns the image-baked default. Resolution:
// plugins.bundled.path config first, TALON_EXTENSIONS_PATH env second,
// /opt/extensions third (Docker convention).
func (h *PluginDepsHandler) bundledExtensionsRoot() string {
	merged, err := config.MergedBytes(h.paths)
	if err == nil {
		if v := gjson.GetBytes(merged, "plugins.bundled.path"); v.Exists() && v.Str != "" {
			return v.Str
		}
	}
	if v := os.Getenv("TALON_EXTENSIONS_PATH"); v != "" {
		return v
	}
	if _, err := os.Stat("/opt/extensions"); err == nil {
		return "/opt/extensions"
	}
	return ""
}

// copyDirectory clones src → dst recursively. We only invoke this
// when promoting a read-only-source extension into the writable
// overlay before npm install; sizes are tens of KB at most so this
// doesn't need to be optimized for huge trees.
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
		// Skip symlinks and special files — extensions are plain JS
		// trees in practice, and we don't want to surprise users with
		// surprise FIFO copies.
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
