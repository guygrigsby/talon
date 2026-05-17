package pkgutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/plugin/pkgutil"
)

func TestResolvePluginCmd_AsIsWhenExists(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "exists")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := pkgutil.ResolvePluginCmd("p", []string{bin, "--flag"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != bin || got[1] != "--flag" {
		t.Errorf("got %v, want [%s --flag]", got, bin)
	}
}

func TestResolvePluginCmd_FallsBackToSibling(t *testing.T) {
	// The test binary's executable dir is os.Executable(); drop a fake
	// plugin binary there and verify it gets picked up when the
	// configured absolute path is missing.
	exe, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable not available")
	}
	dir := filepath.Dir(exe)
	sibling := filepath.Join(dir, "talon-resolve-test-plugin-sibling")
	if err := os.WriteFile(sibling, []byte("x"), 0o755); err != nil {
		t.Skipf("can't write to executable dir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })

	configured := filepath.Join(t.TempDir(), "talon-resolve-test-plugin-sibling")
	got, err := pkgutil.ResolvePluginCmd("p", []string{configured})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != sibling {
		t.Errorf("got %q, want sibling %q", got[0], sibling)
	}
}

func TestResolvePluginCmd_FallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	binName := "talon-resolve-test-plugin-path"
	bin := filepath.Join(dir, binName)
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Use a configured path under a different (nonexistent) directory
	// so as-is and sibling both miss; only PATH lookup should hit.
	configured := filepath.Join(t.TempDir(), binName)
	got, err := pkgutil.ResolvePluginCmd("p", []string{configured})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != bin {
		t.Errorf("got %q, want PATH hit %q", got[0], bin)
	}
}

func TestResolvePluginCmd_EmptyCmd(t *testing.T) {
	if _, err := pkgutil.ResolvePluginCmd("p", nil); err == nil || !strings.Contains(err.Error(), "empty Cmd") {
		t.Errorf("got %v, want empty Cmd error", err)
	}
}
