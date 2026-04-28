package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freezeTime(t *testing.T, ts string) {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", ts)
	if err != nil {
		t.Fatal(err)
	}
	old := nowFunc
	nowFunc = func() time.Time { return parsed }
	t.Cleanup(func() { nowFunc = old })
}

func TestAppend_CreatesFileWithDateHeader(t *testing.T) {
	freezeTime(t, "2026-04-27")
	ws := t.TempDir()

	if err := Append(ws, "first note"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(ws, "memory", "2026-04-27.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.HasPrefix(got, "# 2026-04-27\n\n") {
		t.Errorf("missing date header: %q", got)
	}
	if !strings.Contains(got, "first note") {
		t.Errorf("note missing: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("file should end with newline: %q", got)
	}
}

func TestAppend_AppendsToExistingDayFile(t *testing.T) {
	freezeTime(t, "2026-04-27")
	ws := t.TempDir()

	if err := Append(ws, "first"); err != nil {
		t.Fatal(err)
	}
	if err := Append(ws, "second"); err != nil {
		t.Fatal(err)
	}
	if err := Append(ws, "third"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(ws, "memory", "2026-04-27.md"))
	got := string(body)
	// Header appears once; notes appear in order; separated by blank lines.
	if strings.Count(got, "# 2026-04-27\n") != 1 {
		t.Errorf("date header should appear exactly once: %q", got)
	}
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q missing: %q", want, got)
		}
	}
	// Order check.
	if strings.Index(got, "first") > strings.Index(got, "second") {
		t.Errorf("notes out of order")
	}
	// Blank-line separator between notes.
	if !strings.Contains(got, "first\n\nsecond") {
		t.Errorf("notes should be separated by blank lines: %q", got)
	}
}

func TestAppend_DifferentDaysCreateDifferentFiles(t *testing.T) {
	ws := t.TempDir()

	freezeTime(t, "2026-04-26")
	if err := Append(ws, "yesterday note"); err != nil {
		t.Fatal(err)
	}
	freezeTime(t, "2026-04-27")
	if err := Append(ws, "today note"); err != nil {
		t.Fatal(err)
	}

	for date, want := range map[string]string{
		"2026-04-26": "yesterday note",
		"2026-04-27": "today note",
	} {
		body, err := os.ReadFile(filepath.Join(ws, "memory", date+".md"))
		if err != nil {
			t.Fatalf("missing %s.md: %v", date, err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s.md missing %q", date, want)
		}
	}
}

func TestAppend_RejectsEmptyText(t *testing.T) {
	ws := t.TempDir()
	if err := Append(ws, ""); !errors.Is(err, ErrEmptyText) {
		t.Errorf("got %v, want ErrEmptyText", err)
	}
	if err := Append(ws, "   \n\t  \n"); !errors.Is(err, ErrEmptyText) {
		t.Errorf("whitespace-only text should be rejected, got %v", err)
	}
}

func TestAppend_RejectsEmptyWorkspace(t *testing.T) {
	if err := Append("", "note"); !errors.Is(err, ErrNoWorkspace) {
		t.Errorf("got %v, want ErrNoWorkspace", err)
	}
}

func TestAppend_CreatesMemoryDirIfMissing(t *testing.T) {
	freezeTime(t, "2026-04-27")
	ws := t.TempDir()
	if err := Append(ws, "note"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "memory")); err != nil {
		t.Errorf("memory dir not created: %v", err)
	}
}

func TestAppend_TrimsSurroundingWhitespace(t *testing.T) {
	freezeTime(t, "2026-04-27")
	ws := t.TempDir()
	if err := Append(ws, "\n\n  padded note  \n\n"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(ws, "memory", "2026-04-27.md"))
	if !strings.Contains(string(body), "padded note") {
		t.Errorf("note missing")
	}
	// No leading whitespace runs in note line.
	if strings.Contains(string(body), "  padded note") {
		t.Errorf("padding not trimmed: %q", body)
	}
}
