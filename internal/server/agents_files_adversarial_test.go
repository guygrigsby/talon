package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Adversarial tests for safeWorkspaceFile — the guard that keeps
// agents.files.get/set confined to an agent's workspace. The threat is path
// traversal: a hostile `name` that reads or writes outside the workspace.

func TestSafeWorkspaceFile_RejectsTraversal(t *testing.T) {
	ws := t.TempDir()

	// Names that must be rejected — every one tries to climb out.
	escapes := []string{
		"..",
		"../",
		"../secret",
		"../../etc/passwd",
		"../../../../../../etc/passwd",
		"a/../../b",
		"sub/dir/../../../escape",
		"foo/././../../bar",
		strings.Repeat("../", 64) + "etc/passwd",
	}
	for _, name := range escapes {
		if _, fe := safeWorkspaceFile(ws, name, "test"); fe == nil {
			t.Errorf("TRAVERSAL: name %q was accepted, should escape-reject", name)
		}
	}

	// Empty name is rejected (required).
	if _, fe := safeWorkspaceFile(ws, "", "test"); fe == nil {
		t.Error("empty name should be rejected")
	}
}

// Names that look dangerous but are actually contained must resolve to a path
// inside the workspace — never error, never escape. Absolute-looking names are
// the interesting case: filepath.Join neutralizes the leading slash, so
// "/etc/passwd" becomes <ws>/etc/passwd rather than the system file.
func TestSafeWorkspaceFile_ContainsAbsoluteAndNormalNames(t *testing.T) {
	ws := t.TempDir()
	clean := filepath.Clean(ws)

	contained := []string{
		"file.txt",
		"sub/dir/file.txt",
		"./file.txt",
		"a/b/../c.txt",      // normalizes to a/c.txt, still inside
		"/etc/passwd",       // leading slash neutralized by Join
		"/../../etc/passwd", // Join makes this <ws>/etc/passwd
		"weird\x00name",     // NUL byte: contained lexically; OS open will fail safely
	}
	for _, name := range contained {
		abs, fe := safeWorkspaceFile(ws, name, "test")
		if fe != nil {
			// A reject here is acceptable (fails closed); the contract we
			// must never violate is "accepted AND escaping", checked below.
			continue
		}
		if abs != clean && !strings.HasPrefix(abs, clean+string(filepath.Separator)) {
			t.Errorf("ESCAPE: name %q accepted but resolved outside workspace: %q", name, abs)
		}
	}
}

// FINDING (defense-in-depth gap): safeWorkspaceFile is a purely *lexical*
// check (filepath.Clean/Rel). It does not resolve symlinks, so a symlink that
// lives inside the workspace and points outside passes the guard — the
// returned path is lexically contained but resolves to a file outside the
// workspace. Today this is low-risk because the files API can't create
// symlinks (FilesSet writes regular files), so an attacker can't plant the
// link via the API; it only bites if the workspace already contains one.
// Recommended hardening: resolve with filepath.EvalSymlinks and re-check
// containment before read/write. This test documents the current behavior so
// the gap is visible and a future fix flips the assertion.
func TestSafeWorkspaceFile_SymlinkEscapesLexicalGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	abs, fe := safeWorkspaceFile(ws, "link/secret.txt", "test")
	if fe != nil {
		// If this ever starts rejecting, the gap is closed — update the test.
		t.Logf("lexical guard now rejects symlink traversal (gap closed): %v", fe)
		return
	}

	// Lexically "inside" the workspace...
	clean := filepath.Clean(ws)
	if !strings.HasPrefix(abs, clean+string(filepath.Separator)) {
		t.Fatalf("expected lexically-contained path, got %q", abs)
	}
	// ...but resolves outside it. This is the gap.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	resolvedRoot, _ := filepath.EvalSymlinks(clean)
	if strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		t.Fatalf("expected symlink to resolve OUTSIDE workspace, but it stayed inside: %q", resolved)
	}
	t.Logf("FINDING: %q passed the lexical guard but resolves outside the workspace to %q", "link/secret.txt", resolved)
}
