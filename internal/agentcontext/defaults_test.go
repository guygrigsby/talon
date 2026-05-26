package agentcontext

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEnsureDefaults_EmptyDirWritesAll(t *testing.T) {
	dir := t.TempDir()

	created, err := EnsureDefaults(dir)
	if err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	want := []string{"AGENTS.md", "IDENTITY.md", "SOUL.md", "USER.md"}
	sort.Strings(created)
	if len(created) != len(want) {
		t.Fatalf("created = %v, want %v", created, want)
	}
	for i, n := range want {
		if created[i] != n {
			t.Errorf("created[%d] = %q, want %q", i, created[i], n)
		}
	}

	// Files exist and carry the corrected operator-channel framing,
	// not the openclaw "anything that leaves the machine" rule.
	for _, n := range want {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Errorf("read %s: %v", n, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", n)
		}
	}
	soul, _ := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if !strings.Contains(string(soul), "their own channels") {
		t.Errorf("SOUL.md default missing operator-channel framing:\n%s", soul)
	}
}

func TestEnsureDefaults_NeverOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	const custom = "# my custom identity\nName: Cawdia"
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := EnsureDefaults(dir)
	if err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	// IDENTITY.md must not be in the created set, and its content
	// must be untouched.
	for _, n := range created {
		if n == "IDENTITY.md" {
			t.Errorf("IDENTITY.md should not be overwritten")
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "IDENTITY.md"))
	if string(got) != custom {
		t.Errorf("IDENTITY.md content changed: got %q", got)
	}
	// The other three should have been created.
	if len(created) != 3 {
		t.Errorf("created = %v, want the 3 missing files", created)
	}
}

func TestEnsureDefaults_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaults(dir); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureDefaults(dir)
	if err != nil {
		t.Fatalf("second EnsureDefaults: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second run created %v, want none", created)
	}
}

func TestEnsureDefaults_EmptyDirArg(t *testing.T) {
	created, err := EnsureDefaults("")
	if err != nil {
		t.Fatalf("EnsureDefaults(\"\"): %v", err)
	}
	if created != nil {
		t.Errorf("empty dir should create nothing, got %v", created)
	}
}
