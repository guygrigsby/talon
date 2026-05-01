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
)

// pluginDepsFixture builds a minimal vendored-extensions tree and a
// matching merged-config that points the handler at it. Returns the
// handler ready for direct .handle*() calls.
func pluginDepsFixture(t *testing.T) (*PluginDepsHandler, string) {
	t.Helper()
	paths := readFixture(t, `{}`)
	extRoot := filepath.Join(paths.Talon.Dir, "extensions")
	if err := os.MkdirAll(extRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"plugins":{"bundled":{"path":%q}}}`, extRoot)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
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
	// Build a Paths where every layer in the lookup chain is empty:
	// no talon overlay extensions dir, no openclaw layer at all,
	// no /opt/extensions. The chain returns no items.
	paths := readFixture(t, `{}`)
	paths.SkipOpenclaw = true
	if err := os.WriteFile(paths.Talon.Config, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TALON_EXTENSIONS_PATH", "")

	h := NewPluginDepsHandler(paths)
	// Skip if the test box happens to have /opt/extensions populated
	// (a real dev machine running talon's Docker image locally would).
	if _, err := os.Stat("/opt/extensions"); err == nil {
		t.Skip("test env has /opt/extensions populated")
	}
	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	items := res.(map[string]any)["items"].([]pluginDepsStatusItem)
	// Built-in plugins always surface even when the openclaw lookup
	// chain is empty — that's the point of the registry. Filter
	// them out for this test, which is about chain emptiness.
	openclawOnly := make([]pluginDepsStatusItem, 0)
	for _, it := range items {
		if it.Source != "builtin" {
			openclawOnly = append(openclawOnly, it)
		}
	}
	if len(openclawOnly) != 0 {
		t.Errorf("openclaw items should be empty when no chain sources have content: %+v", openclawOnly)
	}
}

// TestPluginDepsStatus_LookupChainMergesSources verifies the chain
// behavior: talon overlay wins over openclaw layer, both win over
// the bundle.
func TestPluginDepsStatus_LookupChainMergesSources(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := filepath.Join(paths.Talon.Dir, "extensions")
	openclawExt := filepath.Join(paths.Openclaw.Dir, "extensions")
	bundleExt := filepath.Join(paths.Talon.Dir, "fake-bundle")
	for _, d := range []string{talonExt, openclawExt, bundleExt} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(`{"plugins":{"bundled":{"path":%q}}}`, bundleExt)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// "shared" exists in all three layers — talon wins.
	writeExtension(t, talonExt, "shared", nil, false)
	writeExtension(t, openclawExt, "shared", map[string]string{"a": "1"}, true)
	writeExtension(t, bundleExt, "shared", map[string]string{"b": "1"}, true)
	// "user-only" exists only at the openclaw layer.
	writeExtension(t, openclawExt, "user-only", nil, false)
	// "bundled-only" exists only at the bundle layer.
	writeExtension(t, bundleExt, "bundled-only", nil, false)

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
	if bySource["user-only"] != "openclaw" {
		t.Errorf("user-only should resolve to openclaw, got %q", bySource["user-only"])
	}
	if bySource["bundled-only"] != "bundled" {
		t.Errorf("bundled-only should resolve to bundled, got %q", bySource["bundled-only"])
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

// TestPluginDepsInstall_CopiesFromBundleBeforeInstall verifies the
// promotion path: when an extension lives only in the read-only
// bundle, install copies it into the talon overlay first so npm
// install lands somewhere persistent.
func TestPluginDepsInstall_CopiesFromBundleBeforeInstall(t *testing.T) {
	paths := readFixture(t, `{}`)
	bundleExt := filepath.Join(paths.Talon.Dir, "fake-bundle")
	talonExt := filepath.Join(paths.Talon.Dir, "extensions")
	if err := os.MkdirAll(bundleExt, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"plugins":{"bundled":{"path":%q}}}`, bundleExt)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, bundleExt, "needs-deps", map[string]string{"left-pad": "^1.0.0"}, false)

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
		t.Errorf("npm should run in talon overlay copy %q, got %q", expectedDir, npmDir)
	}
	if _, err := os.Stat(filepath.Join(expectedDir, "package.json")); err != nil {
		t.Errorf("package.json should have been copied to talon overlay: %v", err)
	}
	// Source label on the post-install status should report "talon"
	// since the live copy now lives there.
	status := envelope["status"].(pluginDepsStatusItem)
	if status.Source != "talon" {
		t.Errorf("post-install source should be talon, got %q", status.Source)
	}
}

// TestPluginDepsUninstall_RemovesTalonOverlayCopy verifies the
// uninstall path: when the extension lives in the talon overlay,
// remove it; the lookup chain may resurface a bundled copy.
func TestPluginDepsUninstall_RemovesTalonOverlayCopy(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonExt := filepath.Join(paths.Talon.Dir, "extensions")
	bundleExt := filepath.Join(paths.Talon.Dir, "fake-bundle")
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleExt, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"plugins":{"bundled":{"path":%q}}}`, bundleExt)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// "anthropic" exists in BOTH talon overlay and bundle. After
	// uninstall we expect to fall back to the bundle copy.
	writeExtension(t, talonExt, "anthropic", map[string]string{"x": "1"}, true)
	writeExtension(t, bundleExt, "anthropic", map[string]string{"x": "1"}, false)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleUninstall(t.Context(), HandlerCtx{}, []byte(`{"name":"anthropic"}`))
	if ferr != nil {
		t.Fatalf("uninstall: %+v", ferr)
	}
	if res.(map[string]any)["ok"] != true {
		t.Fatalf("ok flag missing: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(talonExt, "anthropic")); !os.IsNotExist(err) {
		t.Errorf("talon-overlay copy should be gone, stat=%v", err)
	}
	status := res.(map[string]any)["status"].(pluginDepsStatusItem)
	if status.Source != "bundled" {
		t.Errorf("post-uninstall source should resurface as bundled, got %q", status.Source)
	}
}

func TestPluginDepsUninstall_RejectsNonTalonSource(t *testing.T) {
	paths := readFixture(t, `{}`)
	bundleExt := filepath.Join(paths.Talon.Dir, "fake-bundle")
	if err := os.MkdirAll(bundleExt, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"plugins":{"bundled":{"path":%q}}}`, bundleExt)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, bundleExt, "anthropic", nil, false)

	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleUninstall(t.Context(), HandlerCtx{}, []byte(`{"name":"anthropic"}`))
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
	talonExt := filepath.Join(paths.Talon.Dir, "extensions")
	if err := os.MkdirAll(talonExt, 0o700); err != nil {
		t.Fatal(err)
	}
	// brave is enabled directly; telegram is configured as a
	// channel (channels.telegram exists) — both should report
	// inUse=true via the two distinct signal paths the helper
	// recognizes.
	cfg := fmt.Sprintf(`{
		"plugins": {
			"bundled": {"path": %q},
			"entries": {"brave": {"enabled": true}}
		},
		"channels": {"telegram": {"agentId": "main"}}
	}`, talonExt)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, talonExt, "brave", nil, false)
	writeExtension(t, talonExt, "telegram", map[string]string{"grammy": "^1.0.0"}, true)
	// Telegram needs an openclaw.channel.id in its package.json
	// for the channels.* signal to match. Re-write its package.json
	// (writeExtension's helper doesn't include the openclaw block).
	pkgWithChannel := `{"name":"@openclaw/telegram","openclaw":{"channel":{"id":"telegram","label":"Telegram"}},"dependencies":{"grammy":"^1.0.0"}}`
	if err := os.WriteFile(filepath.Join(talonExt, "telegram", "package.json"), []byte(pkgWithChannel), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if !byName["brave"].InUse {
		t.Errorf("brave should be inUse via plugins.entries: %+v", byName["brave"])
	}
	if !byName["telegram"].InUse {
		t.Errorf("telegram should be inUse via channels.telegram: %+v", byName["telegram"])
	}
	if byName["idle"].InUse {
		t.Errorf("idle has no config references; should not be inUse")
	}
	// All three live in the talon overlay → uninstallable.
	for _, name := range []string{"brave", "telegram", "idle"} {
		if !byName[name].Uninstallable {
			t.Errorf("%s should be uninstallable (it's in the talon overlay)", name)
		}
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
