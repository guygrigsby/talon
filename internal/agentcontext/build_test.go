package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkWS(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBuild_EmptyWorkspaceReturnsEmpty(t *testing.T) {
	if got := Build(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuild_NoRecognizedFilesReturnsEmpty(t *testing.T) {
	dir := mkWS(t, map[string]string{
		"NOTES.md":  "ignored",
		"random.go": "package x",
	})
	if got := Build(dir); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuild_OrderMatchesOpenclaw(t *testing.T) {
	dir := mkWS(t, map[string]string{
		// Write in reverse-canonical order to confirm Build sorts.
		"MEMORY.md":   "memory body",
		"TOOLS.md":    "tools body",
		"USER.md":     "user body",
		"IDENTITY.md": "identity body",
		"SOUL.md":     "soul body",
		"AGENTS.md":   "agents body",
	})
	got := Build(dir)
	want := []string{"AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md", "TOOLS.md", "MEMORY.md"}
	prev := -1
	for _, w := range want {
		idx := strings.Index(got, "## "+w)
		if idx < 0 {
			t.Errorf("missing section %q in:\n%s", w, got)
			continue
		}
		if idx <= prev {
			t.Errorf("section %q at idx %d, expected after previous (%d)", w, idx, prev)
		}
		prev = idx
	}
}

func TestBuild_PreambleNoticeAlwaysPresent(t *testing.T) {
	dir := mkWS(t, map[string]string{"USER.md": "u"})
	got := Build(dir)
	if !strings.HasPrefix(got, "The following project context files have been loaded:") {
		t.Errorf("missing load notice: %q", got)
	}
}

func TestBuild_SoulPreambleOnlyWhenSoulPresent(t *testing.T) {
	withSoul := Build(mkWS(t, map[string]string{"SOUL.md": "s", "USER.md": "u"}))
	withoutSoul := Build(mkWS(t, map[string]string{"USER.md": "u"}))

	if !strings.Contains(withSoul, "embody its persona") {
		t.Errorf("SOUL.md present but preamble missing:\n%s", withSoul)
	}
	if strings.Contains(withoutSoul, "embody its persona") {
		t.Errorf("SOUL.md absent but preamble appears:\n%s", withoutSoul)
	}
}

func TestBuild_SkipsEmptyOrWhitespaceFiles(t *testing.T) {
	dir := mkWS(t, map[string]string{
		"IDENTITY.md": "",
		"SOUL.md":     "   \n\n  \t  ",
		"USER.md":     "Real content",
	})
	got := Build(dir)
	if strings.Contains(got, "## IDENTITY.md") || strings.Contains(got, "## SOUL.md") {
		t.Errorf("empty/whitespace files should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "## USER.md\n\nReal content") {
		t.Errorf("USER.md content missing:\n%s", got)
	}
	// SOUL preamble must NOT appear since SOUL.md was empty.
	if strings.Contains(got, "embody its persona") {
		t.Errorf("SOUL preamble should not appear when file is empty:\n%s", got)
	}
}

func TestBuild_IgnoresUnknownFiles(t *testing.T) {
	// HEARTBEAT.md and PROJECTS.md exist in real workspaces but aren't
	// part of the canonical chat prompt — they shouldn't bleed in.
	dir := mkWS(t, map[string]string{
		"USER.md":       "u",
		"HEARTBEAT.md":  "should-be-skipped",
		"PROJECTS.md":   "should-be-skipped",
	})
	got := Build(dir)
	if strings.Contains(got, "HEARTBEAT") || strings.Contains(got, "PROJECTS") {
		t.Errorf("unknown files leaked into prompt:\n%s", got)
	}
}

func TestBuild_TrimsTrailingNewlines(t *testing.T) {
	dir := mkWS(t, map[string]string{
		"USER.md": "body\n\n\n",
	})
	got := Build(dir)
	if strings.HasSuffix(got, "\n\n\n") {
		t.Errorf("output should be trimmed of trailing newlines:\n%q", got)
	}
}

// --- recent memory --------------------------------------------------------

func mkMemory(t *testing.T, ws string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(ws, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuild_RecentMemoryNewestFirst(t *testing.T) {
	ws := mkWS(t, map[string]string{"USER.md": "u"})
	mkMemory(t, ws, map[string]string{
		"2026-04-25.md": "older entry",
		"2026-04-26.md": "middle entry",
		"2026-04-27.md": "newest entry",
	})
	got := Build(ws)
	// Section header present.
	if !strings.Contains(got, "## Recent memory") {
		t.Fatalf("missing memory section:\n%s", got)
	}
	newestIdx := strings.Index(got, "newest entry")
	middleIdx := strings.Index(got, "middle entry")
	oldestIdx := strings.Index(got, "older entry")
	if newestIdx < 0 || middleIdx < 0 || oldestIdx < 0 {
		t.Fatalf("memory bodies missing:\n%s", got)
	}
	if !(newestIdx < middleIdx && middleIdx < oldestIdx) {
		t.Errorf("memory entries not sorted newest-first")
	}
}

func TestBuild_RecentMemoryRespectsBudget(t *testing.T) {
	ws := mkWS(t, nil)
	// Three entries that together exceed the budget. Filenames sort
	// reverse-chronologically, so 27 is the newest.
	bigBody := strings.Repeat("x", MemoryBudgetBytes/2+1024)
	mkMemory(t, ws, map[string]string{
		"2026-04-25.md": bigBody,
		"2026-04-26.md": bigBody,
		"2026-04-27.md": bigBody,
	})
	got := Build(ws)

	// Newest entry must be present in full.
	if !strings.Contains(got, "### memory/2026-04-27.md") {
		t.Errorf("newest entry should always be present")
	}
	// Output must be roughly capped — allow some slop for headers and
	// the truncation marker, but not 3× the budget.
	if len(got) > MemoryBudgetBytes*2 {
		t.Errorf("memory section blew the budget: %d bytes", len(got))
	}
	// Either a trailing entry was truncated OR the oldest was dropped
	// entirely. Either is acceptable; just check that we didn't include
	// all three full bodies.
	allThreePresent := strings.Contains(got, "### memory/2026-04-25.md") &&
		strings.Contains(got, "### memory/2026-04-26.md") &&
		strings.Contains(got, "### memory/2026-04-27.md")
	if allThreePresent && !strings.Contains(got, "[truncated to fit memory budget]") {
		t.Errorf("budget exceeded but no truncation marker:\nlen=%d", len(got))
	}
}

func TestBuild_RecentMemoryAlone_NoCanonicalFiles(t *testing.T) {
	ws := mkWS(t, nil)
	mkMemory(t, ws, map[string]string{"2026-04-27.md": "solo entry"})
	got := Build(ws)
	if !strings.Contains(got, "## Recent memory") {
		t.Errorf("memory section should appear even without canonical files:\n%s", got)
	}
	if !strings.Contains(got, "solo entry") {
		t.Errorf("memory body missing:\n%s", got)
	}
	// No load notice for canonical files (none loaded).
	if strings.Contains(got, "The following project context files have been loaded") {
		t.Errorf("load notice should be skipped when no canonical files were loaded:\n%s", got)
	}
}

func TestBuild_RecentMemorySkipsNonMarkdown(t *testing.T) {
	ws := mkWS(t, map[string]string{"USER.md": "u"})
	mkMemory(t, ws, map[string]string{
		"2026-04-27.md":  "real",
		"notes.txt":      "should be skipped",
		"2026-04-27.bak": "should be skipped",
	})
	got := Build(ws)
	if strings.Contains(got, "should be skipped") {
		t.Errorf("non-md files leaked into memory section:\n%s", got)
	}
	if !strings.Contains(got, "real") {
		t.Errorf("md entry missing")
	}
}

func TestBuild_RecentMemorySkipsBlankFiles(t *testing.T) {
	ws := mkWS(t, map[string]string{"USER.md": "u"})
	mkMemory(t, ws, map[string]string{
		"2026-04-27.md": "    \n\n\t  \n",
	})
	got := Build(ws)
	if strings.Contains(got, "## Recent memory") {
		t.Errorf("blank-only memory entry should not produce a section:\n%s", got)
	}
}

func TestBuild_NoMemoryDirIsHarmless(t *testing.T) {
	ws := mkWS(t, map[string]string{"USER.md": "u"})
	// no memory/ dir at all
	got := Build(ws)
	if strings.Contains(got, "## Recent memory") {
		t.Errorf("memory section should be omitted when memory/ is absent:\n%s", got)
	}
}

func TestBuild_RealOpenclawFixture(t *testing.T) {
	// Mirror the real workspace's IDENTITY.md template (whitespace-aware).
	identity := `# IDENTITY.md - Who Am I?

- **Name:** Clawdia
- **Emoji:** 🦞
`
	user := `# USER.md - About Your Human

- **Name:** Guy
- **Timezone:** America/Denver
`
	soul := `# SOUL.md - Who You Are

Have strong opinions. Don't hedge.
`
	dir := mkWS(t, map[string]string{
		"IDENTITY.md": identity,
		"USER.md":     user,
		"SOUL.md":     soul,
	})
	got := Build(dir)

	// Order: SOUL.md (20) before IDENTITY.md (30) before USER.md (40).
	soulIdx := strings.Index(got, "## SOUL.md")
	idIdx := strings.Index(got, "## IDENTITY.md")
	userIdx := strings.Index(got, "## USER.md")
	if !(soulIdx < idIdx && idIdx < userIdx) {
		t.Errorf("ordering wrong (SOUL → IDENTITY → USER):\n%s", got)
	}
	if !strings.Contains(got, "Clawdia") || !strings.Contains(got, "Guy") {
		t.Errorf("content missing:\n%s", got)
	}
}
