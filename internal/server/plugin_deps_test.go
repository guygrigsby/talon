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
	if envelope["bundledPath"] != root {
		t.Errorf("bundledPath wrong: %v", envelope["bundledPath"])
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

func TestPluginDepsStatus_EmptyWhenNoBundledPath(t *testing.T) {
	paths := readFixture(t, `{}`)
	if err := os.WriteFile(paths.Talon.Config, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TALON_EXTENSIONS_PATH", "")
	// Ensure no /opt/extensions exists in test env (it shouldn't on
	// a dev machine, but be explicit).
	h := NewPluginDepsHandler(paths)
	res, ferr := h.handleStatus(t.Context(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	envelope := res.(map[string]any)
	if envelope["bundledPath"] != "" {
		t.Skipf("test env has a real /opt/extensions: %v", envelope["bundledPath"])
	}
	items := envelope["items"].([]pluginDepsStatusItem)
	if len(items) != 0 {
		t.Errorf("items should be empty when no bundledPath: %+v", items)
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

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
