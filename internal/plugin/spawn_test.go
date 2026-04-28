package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestPlugin compiles internal/plugin/testplugin into the test
// process's TempDir and returns the binary path. Cached per-test-package
// in t.TempDir() so each go-test invocation does it once.
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "talon-testplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testplugin")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build testplugin: %v", err)
	}
	return bin
}

// TestLoadPlugin_EndToEnd spawns the real test plugin binary, runs the
// handshake, dials the gRPC server, fetches the manifest, then triggers
// shutdown. Verifies the full subprocess lifecycle path that the unit
// tests skip.
func TestLoadPlugin_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped under -short")
	}
	bin := buildTestPlugin(t)

	h := NewHost("127.0.0.1:18790")
	inst, err := h.LoadPlugin(t.Context(), "testplugin", LoadOptions{
		Cmd: []string{bin},
	})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer h.Unregister("testplugin")

	if inst.Name != "testplugin" {
		t.Errorf("Name = %q, want testplugin", inst.Name)
	}
	if inst.Manifest == nil {
		t.Fatal("manifest is nil")
	}
	if inst.Manifest.Name != "testplugin" || inst.Manifest.Version != "0.1.0" {
		t.Errorf("manifest wrong: %+v", inst.Manifest)
	}
	if len(inst.Manifest.Needs) != 2 {
		t.Errorf("manifest.Needs len = %d, want 2", len(inst.Manifest.Needs))
	}

	// Cookie must be the same one the host minted (subprocess saw it via
	// env, but doesn't echo it; the host stored it on the instance).
	if len(inst.Cookie) != 48 {
		t.Errorf("cookie length = %d", len(inst.Cookie))
	}

	// Registry lookup works.
	if got := h.Get("testplugin"); got != inst {
		t.Errorf("Get returned different instance")
	}
}

func TestLoadPlugin_FailsOnMissingCmd(t *testing.T) {
	h := NewHost("")
	_, err := h.LoadPlugin(t.Context(), "x", LoadOptions{Cmd: nil})
	if err == nil || !strings.Contains(err.Error(), "empty Cmd") {
		t.Errorf("expected empty Cmd rejection, got %v", err)
	}
}

func TestLoadPlugin_FailsOnNonexistentBinary(t *testing.T) {
	h := NewHost("")
	_, err := h.LoadPlugin(t.Context(), "x", LoadOptions{
		Cmd: []string{filepath.Join(t.TempDir(), "does-not-exist")},
	})
	if err == nil {
		t.Errorf("expected error for nonexistent binary")
	}
}

