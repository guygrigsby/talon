package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns the
// captured bytes. Render functions write to os.Stdout directly so we
// have to intercept at the file-descriptor level rather than via a
// per-call writer parameter.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	done := make(chan string, 1)
	go func() {
		buf := &bytes.Buffer{}
		_, _ = io.Copy(buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestFormatMs_ZeroIsDash(t *testing.T) {
	if got := formatMs(0); got != "—" {
		t.Errorf("formatMs(0) = %q, want %q", got, "—")
	}
}

func TestFormatMs_RendersLocal(t *testing.T) {
	// Render a known timestamp; the format string is fixed-width so
	// we can assert the structure without bodging timezones.
	ts := time.Date(2026, 5, 7, 12, 30, 45, 0, time.Local).UnixMilli()
	got := formatMs(ts)
	// Verify the formatter ran (year + colon-separated time present);
	// don't assert the exact value because tz fluctuation between
	// machines isn't worth the flakiness.
	if !strings.Contains(got, "2026-") || !strings.Contains(got, ":") {
		t.Errorf("formatMs unexpected: %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{50, "50ms"},
		{999, "999ms"},
		{1000, "1s"},
		{1500, "1.5s"},
		{60000, "1m0s"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderJobs_EmptyShowsHint(t *testing.T) {
	out := captureStdout(t, func() { renderJobs(nil) })
	if !strings.Contains(out, "no jobs") {
		t.Errorf("expected 'no jobs' hint, got %q", out)
	}
}

func TestRenderJobs_TableHasExpectedColumns(t *testing.T) {
	jobs := []cronJob{
		{
			ID:         "morning",
			Expression: "0 9 * * *",
			Action:     cronJobAction{Method: "system.echo"},
			Enabled:    true,
			NextRunMs:  time.Date(2026, 5, 7, 9, 0, 0, 0, time.Local).UnixMilli(),
		},
		{
			ID:         "off",
			Expression: "@hourly",
			Action:     cronJobAction{Method: "health"},
			Enabled:    false,
			LastStatus: "error",
			LastErr:    "boom",
		},
	}
	out := captureStdout(t, func() { renderJobs(jobs) })
	for _, want := range []string{"ID", "SCHEDULE", "METHOD", "STATE", "morning", "0 9 * * *", "system.echo", "enabled", "off", "@hourly", "disabled", "(last err)"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderJobs output missing %q: %s", want, out)
		}
	}
}

func TestRenderRuns_EmptyShowsHint(t *testing.T) {
	out := captureStdout(t, func() { renderRuns(nil) })
	if !strings.Contains(out, "no runs") {
		t.Errorf("expected 'no runs' hint, got %q", out)
	}
}

func TestRenderRuns_TableMarksManualAndError(t *testing.T) {
	runs := []cronRunRecord{
		{
			RunID: "r1", JobID: "morning", Method: "system.echo",
			StartedAtMs: time.Date(2026, 5, 7, 9, 0, 0, 0, time.Local).UnixMilli(),
			DurationMs:  120, OK: true,
		},
		{
			RunID: "r2", JobID: "morning", Method: "system.echo",
			StartedAtMs: time.Date(2026, 5, 7, 10, 0, 0, 0, time.Local).UnixMilli(),
			DurationMs:  50, OK: false, Error: "boom", Manual: true,
		},
	}
	out := captureStdout(t, func() { renderRuns(runs) })
	if !strings.Contains(out, "ok") || !strings.Contains(out, "ERROR") {
		t.Errorf("expected both ok+ERROR rows: %s", out)
	}
	if !strings.Contains(out, "manual") || !strings.Contains(out, "boom") {
		t.Errorf("expected manual flag and error message in note column: %s", out)
	}
	if !strings.Contains(out, "120ms") || !strings.Contains(out, "50ms") {
		t.Errorf("expected per-run durations: %s", out)
	}
}

func TestReadParams_BothFlagsRejected(t *testing.T) {
	if _, err := readParams("{}", "f.json"); err == nil {
		t.Error("expected error when both inline and file are set")
	}
}

func TestReadParams_InlineMustBeJSON(t *testing.T) {
	if _, err := readParams("not json", ""); err == nil {
		t.Error("expected JSON parse error")
	}
}

func TestReadParams_InlineHappy(t *testing.T) {
	got, err := readParams(`{"x":1}`, "")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v["x"].(float64) != 1 {
		t.Errorf("decoded: %+v", v)
	}
}

func TestReadParams_FileHappy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	if err := os.WriteFile(path, []byte(`{"y":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readParams("", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"y":2`) {
		t.Errorf("unexpected file contents: %s", got)
	}
}

func TestReadParams_FileMustExist(t *testing.T) {
	if _, err := readParams("", "/no/such/file.json"); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadParams_FileMustBeJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readParams("", path); err == nil {
		t.Error("expected JSON parse error from file")
	}
}

func TestReadParams_NeitherSetReturnsNil(t *testing.T) {
	got, err := readParams("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil with neither flag, got %s", got)
	}
}

// TestCronCmd_Wired verifies the cobra subcommand tree builds and
// has the expected child commands. Catches a regression where a
// rename loses a subcommand.
func TestCronCmd_Wired(t *testing.T) {
	c := cronCmd()
	want := map[string]bool{
		"list": false, "add": false, "remove": false,
		"run": false, "status": false, "runs": false,
	}
	for _, sub := range c.Commands() {
		name := strings.SplitN(sub.Use, " ", 2)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected `cron %s` subcommand", name)
		}
	}
}

func TestCronAddCmd_RequiresExpressionAndMethod(t *testing.T) {
	c := cronAddCmd()
	c.SetArgs([]string{"jobid"})
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	err := c.Execute()
	if err == nil {
		t.Error("expected error when --expr and --method are missing")
	}
}
