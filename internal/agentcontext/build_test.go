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

func TestBuild_LoadsCanonicalSetInOrder(t *testing.T) {
	dir := mkWS(t, map[string]string{
		// Write in reverse-canonical order to confirm Build sorts.
		"USER.md":     "user body",
		"IDENTITY.md": "identity body",
		"SOUL.md":     "soul body",
		"AGENTS.md":   "agents body",
		// Non-canonical files — should NOT appear.
		"NOTES.md":   "notes body",
		"random.md":  "random body",
	})
	got := Build(dir)
	want := []string{"AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md"}
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
	}
	for _, nonCanonical := range []string{"## NOTES.md", "## random.md"} {
		if strings.Contains(got, nonCanonical) {
			t.Errorf("non-canonical file leaked into prompt: %s", nonCanonical)
		}
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

// --- recent memory (REMOVED) -------------------------------------
//
// The workspace memory/*.md scan was retired with the RAG store;
// the prior TestBuild_RecentMemory* tests went with it. Memory now
// lives in ~/.talon/memory/ via the `remember` tool, and the
// workspace tools (write/edit) deny-list any path under
// <workspace>/memory/ to keep the model from recreating the
// legacy pattern.

func TestBuild_PersonaFilesFixture(t *testing.T) {
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
