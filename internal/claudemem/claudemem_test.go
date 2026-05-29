package claudemem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed writes the given files into a fresh temp dir and returns the dir.
func seed(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, body := range files {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return d
}

func TestNew_RejectsMissingDir(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("New on missing dir should error")
	}
}

func TestNew_RejectsFile(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": "x"})
	if _, err := New(filepath.Join(d, "MEMORY.md")); err == nil {
		t.Fatal("New on a file should error")
	}
}

func TestStore_IndexCapTruncates(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": strings.Repeat("- line\n", 1000)})
	s, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := s.Index(100)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(got) > 200 || !strings.Contains(got, "claude_memory") {
		t.Fatalf("not capped/marked: len=%d body=%q", len(got), got)
	}
}

func TestStore_IndexUncapped(t *testing.T) {
	body := strings.Repeat("- line\n", 10)
	d := seed(t, map[string]string{"MEMORY.md": body})
	s, _ := New(d)
	got, err := s.Index(0)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if got != body {
		t.Fatalf("uncapped index = %q, want %q", got, body)
	}
}

func TestStore_IndexMissingIsEmpty(t *testing.T) {
	d := seed(t, map[string]string{"feedback_x.md": "y"})
	s, _ := New(d)
	got, err := s.Index(4096)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if got != "" {
		t.Fatalf("missing MEMORY.md should yield empty index, got %q", got)
	}
}

func TestStore_ReadRejectsTraversal(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": "x", "feedback_x.md": "secret-ok body"})
	s, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, bad := range []string{"../../etc/passwd", "..", "a/b", `a\b`, "../feedback_x", "/etc/passwd"} {
		if _, err := s.Read(bad, 4096); err == nil {
			t.Fatalf("traversal allowed for slug %q", bad)
		}
	}
	got, err := s.Read("feedback_x", 4096)
	if err != nil {
		t.Fatalf("legit read failed: %v", err)
	}
	if got != "secret-ok body" {
		t.Fatalf("read = %q, want %q", got, "secret-ok body")
	}
}

func TestStore_ReadCaps(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": "x", "feedback_x.md": strings.Repeat("- line\n", 1000)})
	s, _ := New(d)
	got, err := s.Read("feedback_x", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) > 200 || !strings.Contains(got, "claude_memory") {
		t.Fatalf("read not capped/marked: len=%d", len(got))
	}
}

func TestStore_ListExcludesIndex(t *testing.T) {
	d := seed(t, map[string]string{
		"MEMORY.md":     "x",
		"feedback_x.md": "y",
		"project_z.md":  "z",
		"notes.txt":     "ignored",
	})
	s, _ := New(d)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]bool{"feedback_x": true, "project_z": true}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want keys %v", got, want)
	}
	for _, slug := range got {
		if !want[slug] {
			t.Fatalf("unexpected slug %q in %v", slug, got)
		}
		if slug == "MEMORY" {
			t.Fatal("MEMORY must be excluded from List")
		}
	}
}
