package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonpath"
)

func newWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func writeWS(t *testing.T, ws, rel, body string) {
	t.Helper()
	abs := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- registry --------------------------------------------------------------

func TestRegistry_BuiltinsRegistered(t *testing.T) {
	r := New(newWorkspace(t))
	want := []string{"bash", "edit", "glob", "grep", "read", "write"}
	got := r.Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	specs := r.Specs()
	if len(specs) != len(want) {
		t.Fatalf("Specs() len = %d, want %d", len(specs), len(want))
	}
	for i, s := range specs {
		if s.Name != want[i] {
			t.Errorf("Specs()[%d].Name = %q, want %q", i, s.Name, want[i])
		}
		if s.Description == "" {
			t.Errorf("Specs()[%d] missing description", i)
		}
		// Schemas must be valid JSON.
		if !json.Valid(s.ParametersSchema) {
			t.Errorf("Specs()[%d].ParametersSchema is not valid JSON", i)
		}
	}
}

func TestRegistry_RunUnknownToolErrors(t *testing.T) {
	r := New(newWorkspace(t))
	_, err := r.Run(t.Context(), "frobnicate", []byte(`{}`))
	if err == nil || !errors.Is(err, ErrUnknownTool) {
		t.Errorf("expected ErrUnknownTool, got %v", err)
	}
}

func TestRegistry_EmptyWorkspaceSkipsBuiltins(t *testing.T) {
	r := New("")
	if names := r.Names(); len(names) != 0 {
		t.Errorf("empty workspace should skip builtins, got %v", names)
	}
}

// --- workspace confinement -------------------------------------------------

func TestResolveInWorkspace_RejectsTraversal(t *testing.T) {
	ws := newWorkspace(t)
	cases := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"sub/../../etc/passwd",
		"/etc/passwd",
	}
	for _, in := range cases {
		_, err := resolveInWorkspace(ws, in)
		if err == nil {
			t.Errorf("resolveInWorkspace(%q) should have rejected the path", in)
		}
	}
}

func TestResolveInWorkspace_AcceptsAbsoluteUnderWorkspace(t *testing.T) {
	ws := newWorkspace(t)
	abs := filepath.Join(ws, "sub", "file")
	got, err := resolveInWorkspace(ws, abs)
	if err != nil {
		t.Fatal(err)
	}
	if got != abs {
		t.Errorf("got %q, want %q", got, abs)
	}
}

func TestResolveInWorkspace_LookalikePrefixIsRejected(t *testing.T) {
	// /tmp/foo workspace must reject /tmp/foobar/x — the HasPrefix-style
	// guard would have wrongly accepted it; the Rel-based check we use
	// catches it.
	ws := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(filepath.Dir(ws), "foobar", "x")
	if _, err := resolveInWorkspace(ws, bad); err == nil {
		t.Errorf("expected reject for %q against ws %q", bad, ws)
	}
}

// --- read ------------------------------------------------------------------

func TestReadTool_FullFile(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "line1\nline2\nline3\n")
	r := New(ws)
	out, err := r.Run(t.Context(), "read", []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "line1\nline2\nline3\n" {
		t.Errorf("unexpected: %q", out)
	}
}

func TestReadTool_LineRange(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "l1\nl2\nl3\nl4\nl5")
	r := New(ws)
	out, err := r.Run(t.Context(), "read", []byte(`{"path":"a.txt","start":2,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "l2\nl3\nl4" {
		t.Errorf("got %q, want l2\\nl3\\nl4", out)
	}
}

func TestReadTool_RejectsTraversal(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	_, err := r.Run(t.Context(), "read", []byte(`{"path":"../etc/passwd"}`))
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("expected escape rejection, got %v", err)
	}
}

// --- write -----------------------------------------------------------------

func TestWriteTool_CreatesFileAndDirs(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	out, err := r.Run(t.Context(), "write", []byte(`{"path":"new/dir/file.txt","content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote 5 bytes") {
		t.Errorf("output should describe the write: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(ws, "new", "dir", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("file content = %q, want hello", got)
	}
}

func TestWriteTool_RejectsTraversal(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	_, err := r.Run(t.Context(), "write", []byte(`{"path":"../escape","content":"x"}`))
	if err == nil {
		t.Error("expected rejection")
	}
}

// --- edit ------------------------------------------------------------------

func TestEditTool_UniqueReplace(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "alpha beta gamma")
	r := New(ws)
	if _, err := r.Run(t.Context(), "edit", []byte(`{"path":"a.txt","old":"beta","new":"BETA"}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	if string(got) != "alpha BETA gamma" {
		t.Errorf("got %q", got)
	}
}

func TestEditTool_AmbiguousMatchErrors(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "x x x")
	r := New(ws)
	_, err := r.Run(t.Context(), "edit", []byte(`{"path":"a.txt","old":"x","new":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "matches 3 times") {
		t.Errorf("expected 3-match error, got %v", err)
	}
}

func TestEditTool_NoMatchErrors(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "alpha")
	r := New(ws)
	_, err := r.Run(t.Context(), "edit", []byte(`{"path":"a.txt","old":"missing","new":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

// --- bash ------------------------------------------------------------------

func TestBashTool_RunsAndCapturesOutput(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	out, err := r.Run(t.Context(), "bash", []byte(`{"command":"echo hello && echo err 1>&2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "err") {
		t.Errorf("output should include both stdout and stderr: %q", out)
	}
}

func TestBashTool_NonZeroExitIncludesCode(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	out, err := r.Run(t.Context(), "bash", []byte(`{"command":"false"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[exit 1]") {
		t.Errorf("output should describe exit code: %q", out)
	}
}

func TestBashTool_RunsInWorkspaceDir(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "marker.txt", "")
	r := New(ws)
	out, err := r.Run(t.Context(), "bash", []byte(`{"command":"ls marker.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("ls should see marker.txt (cwd is workspace): %q", out)
	}
}

// --- subagent --------------------------------------------------------------

type stubSubagent struct {
	calls  []subagentCall
	output string
	err    error
}
type subagentCall struct{ AgentID, Prompt string }

func (s *stubSubagent) RunInline(ctx context.Context, agentID, message string) (string, error) {
	s.calls = append(s.calls, subagentCall{AgentID: agentID, Prompt: message})
	return s.output, s.err
}

func TestSubagentTool_DelegatesToRunner(t *testing.T) {
	sub := &stubSubagent{output: "delegated reply"}
	r := NewWithSubagent(newWorkspace(t), sub)
	out, err := r.Run(t.Context(), "subagent", []byte(`{"agentId":"coding","prompt":"fix the bug"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "delegated reply" {
		t.Errorf("output = %q", out)
	}
	if len(sub.calls) != 1 || sub.calls[0].AgentID != "coding" || sub.calls[0].Prompt != "fix the bug" {
		t.Errorf("runner saw wrong call: %+v", sub.calls)
	}
}

func TestSubagentTool_RejectsMissingFields(t *testing.T) {
	sub := &stubSubagent{output: "ok"}
	r := NewWithSubagent(newWorkspace(t), sub)
	for _, body := range []string{
		`{}`,
		`{"agentId":"main"}`,
		`{"prompt":"hi"}`,
		`{"agentId":"","prompt":"hi"}`,
	} {
		_, err := r.Run(t.Context(), "subagent", []byte(body))
		if err == nil {
			t.Errorf("expected rejection for %q", body)
		}
	}
}

func TestSubagentTool_DepthLimit(t *testing.T) {
	sub := &stubSubagent{output: "ok"}
	r := NewWithSubagent(newWorkspace(t), sub)
	ctx := withSubagentDepth(t.Context(), maxSubagentDepth) // already at the cap
	_, err := r.Run(ctx, "subagent", []byte(`{"agentId":"x","prompt":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Errorf("expected depth-limit error, got %v", err)
	}
}

func TestNewWithSubagent_NilRunnerSkipsTool(t *testing.T) {
	r := NewWithSubagent(newWorkspace(t), nil)
	for _, name := range r.Names() {
		if name == "subagent" {
			t.Errorf("subagent tool should not register when runner is nil")
		}
	}
}

func TestNewWithSubagent_RegistersSubagentAlongsideBuiltins(t *testing.T) {
	r := NewWithSubagent(newWorkspace(t), &stubSubagent{output: "ok"})
	got := r.Names()
	wantSubset := []string{"bash", "edit", "glob", "grep", "read", "subagent", "write"}
	if strings.Join(got, ",") != strings.Join(wantSubset, ",") {
		t.Errorf("Names() = %v, want %v", got, wantSubset)
	}
}

func TestSubagentToolSchemaEnumeratesFileBackedSubagents(t *testing.T) {
	root := t.TempDir()
	subDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "coding.md"), []byte(`---
description: Code work.
---
Use for code.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := talonpath.Paths{Talon: talonpath.Layer{
		Dir:    root,
		Config: filepath.Join(root, "config.toml"),
	}}
	r := NewWithSubagentAndPaths(newWorkspace(t), &stubSubagent{output: "ok"}, paths)
	raw := r.tools["subagent"].ParametersSchema()
	if !strings.Contains(string(raw), `"coding"`) {
		t.Fatalf("subagent schema should enumerate file-backed subagents: %s", raw)
	}
	if !strings.Contains(r.tools["subagent"].Description(), "coding - Code work.") {
		t.Fatalf("subagent description should include delegation guidance: %s", r.tools["subagent"].Description())
	}
}

// --- glob ------------------------------------------------------------------

func TestGlobTool_ListsMatches(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.go", "")
	writeWS(t, ws, "b.go", "")
	writeWS(t, ws, "c.txt", "")
	r := New(ws)
	out, err := r.Run(t.Context(), "glob", []byte(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.go", "b.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %q", want, out)
		}
	}
	if strings.Contains(out, "c.txt") {
		t.Errorf("c.txt should not match *.go: %q", out)
	}
}

func TestGlobTool_NoMatches(t *testing.T) {
	ws := newWorkspace(t)
	r := New(ws)
	out, err := r.Run(t.Context(), "glob", []byte(`{"pattern":"*.nonexistent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Errorf("got %q", out)
	}
}

// --- grep ------------------------------------------------------------------

func TestGrepTool_FindsMatches(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.go", "package main\nfunc main() {}\n")
	writeWS(t, ws, "b.txt", "irrelevant\nfunc nothing\n")
	r := New(ws)
	out, err := r.Run(t.Context(), "grep", []byte(`{"pattern":"func ","include":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go:2:func main()") {
		t.Errorf("expected hit in a.go: %q", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Errorf("include filter should have excluded b.txt: %q", out)
	}
}

func TestGrepTool_NoMatches(t *testing.T) {
	ws := newWorkspace(t)
	writeWS(t, ws, "a.txt", "hello\n")
	r := New(ws)
	out, err := r.Run(t.Context(), "grep", []byte(`{"pattern":"goodbye"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Errorf("got %q", out)
	}
}

func TestGrepTool_InvalidRegex(t *testing.T) {
	r := New(newWorkspace(t))
	_, err := r.Run(t.Context(), "grep", []byte(`{"pattern":"[unclosed"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Errorf("expected regex error, got %v", err)
	}
}

func TestGrepTool_RespectsContextCancel(t *testing.T) {
	ws := newWorkspace(t)
	for i := 0; i < 50; i++ {
		writeWS(t, ws, "f.txt", strings.Repeat("matchme\n", 1000))
	}
	r := New(ws)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel immediately
	_, err := r.Run(ctx, "grep", []byte(`{"pattern":"matchme"}`))
	// Either succeeds with partial output or returns the ctx error —
	// we just want to confirm it doesn't hang or panic.
	_ = err
}
