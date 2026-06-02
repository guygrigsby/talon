package toolgate

import (
	"testing"

	"github.com/guygrigsby/pinion/effect"
)

// scopesEqual compares an effect slice to an expected (kind, pattern) table,
// order-independent, so tests read as a set of declared capabilities.
func hasEffect(got []effect.Effect, kind effect.Kind, pattern string) bool {
	for _, e := range got {
		if e.Kind == kind && e.Scope.Pattern == pattern {
			return true
		}
	}
	return false
}

func TestEffectsFor(t *testing.T) {
	const ws = "/work"
	cases := []struct {
		name     string
		tool     string
		args     string
		wantKind effect.Kind
		wantScop string // "" means unscoped
		// for unknown/plugin tools we assert MaxDanger separately
	}{
		{"read relative", "read", `{"file_path":"a.txt"}`, effect.FSRead, "/work/a.txt"},
		{"read absolute", "read", `{"file_path":"/etc/passwd"}`, effect.FSRead, "/etc/passwd"},
		{"read nested relative", "read", `{"file_path":"sub/dir/f.go"}`, effect.FSRead, "/work/sub/dir/f.go"},
		{"write relative", "write", `{"file_path":"out.txt"}`, effect.FSWrite, "/work/out.txt"},
		{"edit relative", "edit", `{"file_path":"x.go"}`, effect.FSWrite, "/work/x.go"},
		{"bash is exec unscoped", "bash", `{"command":"rm -rf /"}`, effect.Exec, ""},
		{"grep with path", "grep", `{"pattern":"foo","path":"sub"}`, effect.FSRead, "/work/sub"},
		{"ls with path", "ls", `{"path":"sub"}`, effect.FSRead, "/work/sub"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EffectsFor(c.tool, []byte(c.args), ws)
			if len(got) != 1 {
				t.Fatalf("EffectsFor(%s) = %d effects, want 1: %+v", c.tool, len(got), got)
			}
			if !hasEffect(got, c.wantKind, c.wantScop) {
				t.Fatalf("EffectsFor(%s)=%+v, want kind=%s scope=%q", c.tool, got, c.wantKind, c.wantScop)
			}
		})
	}
}

// grep/glob default to the working directory (the workspace) when no path is
// given; the scope is the recursive workspace glob, which the workspace grant
// covers. This keeps a normal whole-workspace search from being denied.
func TestEffectsForDefaultsToWorkspaceGlob(t *testing.T) {
	const ws = "/work"
	for _, tool := range []string{"grep", "glob"} {
		got := EffectsFor(tool, []byte(`{"pattern":"foo"}`), ws)
		if !hasEffect(got, effect.FSRead, "/work/**") {
			t.Fatalf("EffectsFor(%s no path)=%+v, want fs.read /work/**", tool, got)
		}
	}
}

// An unknown tool (a plugin that declared no effects) is treated as
// MaxDanger: every source and sink, unscoped, so any flow lights up.
func TestEffectsForUnknownToolIsMaxDanger(t *testing.T) {
	got := EffectsFor("frobnicate", []byte(`{}`), "/work")
	want := effect.MaxDanger()
	if len(got) != len(want) {
		t.Fatalf("unknown tool effects = %d, want MaxDanger %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unknown tool effects[%d]=%v, want %v", i, got[i], want[i])
		}
	}
}

// claude_memory reads talon's notes store, which lives outside the agent
// workspace; it maps to an unscoped fs.read so the workspace grant does not
// silently cover it (read-only, but out of scope by default).
func TestEffectsForClaudeMemoryIsUnscopedRead(t *testing.T) {
	got := EffectsFor("claude_memory", []byte(`{"op":"read","slug":"x"}`), "/work")
	if len(got) != 1 || !hasEffect(got, effect.FSRead, "") {
		t.Fatalf("claude_memory effects=%+v, want single unscoped fs.read", got)
	}
}

// Unparseable or empty args for a known fs tool yield an unscoped effect (the
// widest request), never a panic — conservative, can only tighten the verdict.
func TestEffectsForBadArgsAreUnscoped(t *testing.T) {
	got := EffectsFor("read", []byte(`not json`), "/work")
	if len(got) != 1 || !hasEffect(got, effect.FSRead, "") {
		t.Fatalf("read with bad args=%+v, want single unscoped fs.read", got)
	}
	got = EffectsFor("read", nil, "/work")
	if len(got) != 1 || !hasEffect(got, effect.FSRead, "") {
		t.Fatalf("read with nil args=%+v, want single unscoped fs.read", got)
	}
}
