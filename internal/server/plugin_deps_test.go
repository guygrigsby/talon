package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonconfig"
)

// pluginDepsFixture builds a minimal third-party plugin tree and a
// matching merged-config that points the handler at it. Returns the
// handler ready for direct .handle*() calls.
func pluginDepsFixture(t *testing.T) (*PluginDepsHandler, string) {
	t.Helper()
	paths := readFixture(t, `{}`)
	extRoot := paths.Talon.PluginsDir()
	if err := os.MkdirAll(extRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewPluginDepsHandler(paths), extRoot
}

// writeExtension lays out one fake extension: <root>/<name>/package.json
// (with optional dependencies). When installed=true we also create a
// node_modules dir so statusForExtension reports installed.
func writeExtension(t *testing.T, root, name string, deps map[string]string, installed bool) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{"name": "@fixture/" + name}
	if deps != nil {
		pkg["dependencies"] = deps
	}
	body := mustMarshal(t, pkg)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if installed {
		if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPluginDepsStatus_ReportsPerExtension(t *testing.T) {
	h, root := pluginDepsFixture(t)
	writeExtension(t, root, "ready-no-deps", nil, false)
	writeExtension(t, root, "needs-install", map[string]string{"axios": "^1.0.0"}, false)
	writeExtension(t, root, "already-installed", map[string]string{"zod": "^3.0.0"}, true)
	// A directory without package.json is still listed but shows
	// installed=true (nothing to install).
	if err := os.MkdirAll(filepath.Join(root, "no-package-json"), 0o700); err != nil {
		t.Fatal(err)
	}

	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["writeRoot"] != root {
		t.Errorf("writeRoot wrong: %v (want %q)", envelope["writeRoot"], root)
	}
	items := envelope["items"].([]pluginDepsStatusItem)
	byName := map[string]pluginDepsStatusItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	for _, want := range []string{"ready-no-deps", "needs-install", "already-installed", "no-package-json"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("missing %q in status: %v", want, items)
		}
	}
	if !byName["ready-no-deps"].Installed || byName["ready-no-deps"].DepCount != 0 {
		t.Errorf("ready-no-deps wrong: %+v", byName["ready-no-deps"])
	}
	if byName["needs-install"].Installed || byName["needs-install"].DepCount != 1 {
		t.Errorf("needs-install wrong: %+v", byName["needs-install"])
	}
	if !byName["already-installed"].Installed || byName["already-installed"].DepCount != 1 {
		t.Errorf("already-installed wrong: %+v", byName["already-installed"])
	}
	if !byName["no-package-json"].Installed || byName["no-package-json"].HasPackageJSON {
		t.Errorf("no-package-json wrong: %+v", byName["no-package-json"])
	}
}

func TestPluginDepsStatus_EmptyWhenNoSources(t *testing.T) {
	// Build a Paths where every optional plugin metadata source is empty.
	paths := readFixture(t, `{}`)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	items := res.(map[string]any)["items"].([]pluginDepsStatusItem)
	// Built-in plugins always surface. Filter them out for this
	// test, which is about optional metadata-source emptiness.
	externalOnly := make([]pluginDepsStatusItem, 0)
	for _, it := range items {
		if it.Source != "builtin" {
			externalOnly = append(externalOnly, it)
		}
	}
	if len(externalOnly) != 0 {
		t.Errorf("external items should be empty when no sources have content: %+v", externalOnly)
	}
}

func TestPluginDepsStatus_ListsTalonPluginDir(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := paths.Talon.PluginsDir()
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, talonExt, "shared", nil, false)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	items := res.(map[string]any)["items"].([]pluginDepsStatusItem)
	bySource := map[string]string{}
	for _, it := range items {
		bySource[it.Name] = it.Source
	}
	if bySource["shared"] != "talon" {
		t.Errorf("shared should resolve to talon, got %q (full=%+v)", bySource["shared"], items)
	}
}

func TestPluginDepsInstall_RunsNpmAndReportsResult(t *testing.T) {
	h, root := pluginDepsFixture(t)
	writeExtension(t, root, "needs-install", map[string]string{"left-pad": "^1.0.0"}, false)

	// Stub npm: just make node_modules and exit 0.
	h.WithNpmCmd(func(ctx context.Context, dir string) *exec.Cmd {
		c := exec.CommandContext(ctx, "sh", "-c",
			"mkdir -p \""+dir+"/node_modules\" && echo 'fake npm: added 1 package'")
		return c
	})

	res, ferr := h.handleInstall(t.Context(), HandlerCtx{}, []byte(`{"name":"needs-install"}`))
	if ferr != nil {
		t.Fatalf("install: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != true || envelope["name"] != "needs-install" {
		t.Errorf("install envelope wrong: %+v", envelope)
	}
	output, _ := envelope["output"].(string)
	if !strings.Contains(output, "fake npm") {
		t.Errorf("output not captured: %q", output)
	}
	status := envelope["status"].(pluginDepsStatusItem)
	if !status.Installed {
		t.Errorf("post-install status should report installed: %+v", status)
	}
}

func TestPluginDepsInstall_NpmErrorSurfacedNonFailing(t *testing.T) {
	h, root := pluginDepsFixture(t)
	writeExtension(t, root, "broken", map[string]string{"nope": "^9.9.9"}, false)

	h.WithNpmCmd(func(ctx context.Context, dir string) *exec.Cmd {
		// Exit non-zero to simulate npm failure.
		return exec.CommandContext(ctx, "sh", "-c", "echo 'npm ERR! 404 nope' >&2; exit 1")
	})

	res, ferr := h.handleInstall(t.Context(), HandlerCtx{}, []byte(`{"name":"broken"}`))
	if ferr != nil {
		// We deliberately return ok=false rather than an RPC error so
		// the UI can render the failure inline; an RPC error would
		// trigger generic error handling instead.
		t.Fatalf("install should not RPC-fail on npm error: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != false {
		t.Errorf("ok should be false on npm error: %+v", envelope)
	}
	if !strings.Contains(envelope["output"].(string), "npm ERR!") {
		t.Errorf("error output not captured: %v", envelope["output"])
	}
}

func TestPluginDepsInstall_RejectsTraversal(t *testing.T) {
	h, _ := pluginDepsFixture(t)
	for _, bad := range []string{"", "..", ".", "anthropic/../malicious", `..\windows`} {
		_, ferr := h.handleInstall(t.Context(), HandlerCtx{},
			[]byte(fmt.Sprintf(`{"name":%q}`, bad)))
		if ferr == nil || ferr.Code != ErrCodeBadRequest {
			t.Errorf("name=%q: expected BAD_REQUEST, got %+v", bad, ferr)
		}
	}
}

func TestPluginDepsInstall_NoPackageJSONIsNoOp(t *testing.T) {
	h, root := pluginDepsFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "no-pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, ferr := h.handleInstall(t.Context(), HandlerCtx{}, []byte(`{"name":"no-pkg"}`))
	if ferr != nil {
		t.Fatalf("install: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != true || envelope["skipped"] != "no package.json" {
		t.Errorf("expected skipped no-op, got %+v", envelope)
	}
}

func TestPluginDepsInstall_UsesPluginsDir(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := paths.Talon.PluginsDir()
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, talonExt, "needs-deps", map[string]string{"left-pad": "^1.0.0"}, false)

	var npmDir string
	h := NewPluginDepsHandler(paths)
	h.WithNpmCmd(func(ctx context.Context, dir string) *exec.Cmd {
		npmDir = dir
		return exec.CommandContext(ctx, "sh", "-c",
			"mkdir -p \""+dir+"/node_modules\" && echo 'fake npm: added 1 package'")
	})

	res, ferr := h.handleInstall(t.Context(), HandlerCtx{}, []byte(`{"name":"needs-deps"}`))
	if ferr != nil {
		t.Fatalf("install: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != true {
		t.Fatalf("install not ok: %+v", envelope)
	}
	expectedDir := filepath.Join(talonExt, "needs-deps")
	if npmDir != expectedDir {
		t.Errorf("npm should run in plugin dir %q, got %q", expectedDir, npmDir)
	}
	if _, err := os.Stat(filepath.Join(expectedDir, "package.json")); err != nil {
		t.Errorf("package.json should have been copied to talon overlay: %v", err)
	}
	status := envelope["status"].(pluginDepsStatusItem)
	if status.Source != "talon" {
		t.Errorf("post-install source should be talon, got %q", status.Source)
	}
}

// TestPluginDepsUninstall_RemovesTalonOverlayCopy verifies the
// uninstall path: when the plugin lives in Talon's plugin dir, remove it.
func TestPluginDepsUninstall_RemovesTalonOverlayCopy(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := paths.Talon.PluginsDir()
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, talonExt, "anthropic", map[string]string{"x": "1"}, true)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleUninstall(t.Context(), HandlerCtx{}, []byte(`{"name":"anthropic"}`))
	if ferr != nil {
		t.Fatalf("uninstall: %+v", ferr)
	}
	if res.(map[string]any)["ok"] != true {
		t.Fatalf("ok flag missing: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(talonExt, "anthropic")); !os.IsNotExist(err) {
		t.Errorf("plugin dir copy should be gone, stat=%v", err)
	}
}

func TestPluginDepsUninstall_BuiltinIsNoOp(t *testing.T) {
	paths := readFixture(t, `{}`)
	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleUninstall(t.Context(), HandlerCtx{}, []byte(`{"name":"telegram"}`))
	if ferr != nil {
		t.Fatalf("uninstall: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != true {
		t.Errorf("expected ok=true on no-op uninstall, got %+v", envelope)
	}
	if envelope["skipped"] != "not present in talon overlay" {
		t.Errorf("expected skipped reason, got %+v", envelope)
	}
}

func TestPluginDepsStatus_FlagsInUseFromConfig(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := paths.Talon.PluginsDir()
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	// matrix is enabled directly. telegram exists too, but as a builtin:
	// its channels.<name> signal goes through a separate code path.
	// matrix is used here instead of brave because brave is also a builtin.
	cfg := `{
		"plugins": {
			"entries": {"matrix": {"enabled": true}}
		},
		"channels": {"telegram": {"agentId": "main"}}
	}`
	native, err := talonconfig.FromRuntimeJSON([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Talon.Config, talonconfig.MarshalTOML(native, talonconfig.MarshalOptions{}), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, talonExt, "matrix", nil, false)
	// idle: an extension that isn't referenced anywhere.
	writeExtension(t, talonExt, "idle", nil, false)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	items := res.(map[string]any)["items"].([]pluginDepsStatusItem)
	byName := map[string]pluginDepsStatusItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if !byName["matrix"].InUse {
		t.Errorf("matrix should be inUse via plugins.entries: %+v", byName["matrix"])
	}
	// Builtin telegram has channels.telegram set but no package.json —
	// the inUse helper recognizes builtin channel plugins by their
	// kind+name pair (separate code path from the npm extension above).
	if !byName["telegram"].InUse {
		t.Errorf("builtin telegram should be inUse via channels.telegram: %+v", byName["telegram"])
	}
	if byName["idle"].InUse {
		t.Errorf("idle has no config references; should not be inUse")
	}
	// Third-party plugin directories live in Talon state and are uninstallable.
	// Builtin telegram is a shipped binary (not in the overlay), so
	// the row is intentionally NOT uninstallable.
	for _, name := range []string{"matrix", "idle"} {
		if !byName[name].Uninstallable {
			t.Errorf("%s should be uninstallable (it's in the talon overlay)", name)
		}
	}
	if byName["telegram"].Uninstallable {
		t.Errorf("builtin telegram should not be uninstallable: %+v", byName["telegram"])
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
