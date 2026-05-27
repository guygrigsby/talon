package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/audit"
)

func TestFilterAuditEvents_BySessionAndRun(t *testing.T) {
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	events := []audit.Event{
		{Ts: base.Add(3 * time.Second), Kind: audit.KindToolResult, Session: "s1", Run: "r1", Seq: 2, Tool: "bash"},
		{Ts: base.Add(1 * time.Second), Kind: audit.KindTurnStart, Session: "s1", Run: "r1", Seq: 1},
		{Ts: base.Add(2 * time.Second), Kind: audit.KindToolCall, Session: "s1", Run: "r2", Seq: 1, Tool: "edit"},
		{Ts: base.Add(4 * time.Second), Kind: audit.KindTurnStart, Session: "s2", Run: "r1", Seq: 1},
	}

	got := filterAuditEvents(events, "s1", "r1", time.Time{})
	if len(got) != 2 {
		t.Fatalf("expected 2 events for s1/r1, got %d: %+v", len(got), got)
	}
	// Sorted by Ts, so turn_start (seq 1) precedes tool_result (seq 2).
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("expected seq order 1,2, got %d,%d", got[0].Seq, got[1].Seq)
	}
	for _, e := range got {
		if e.Session != "s1" || e.Run != "r1" {
			t.Fatalf("filter leaked a non-matching event: %+v", e)
		}
	}

	// Session-only filter spans both runs of s1.
	if n := len(filterAuditEvents(events, "s1", "", time.Time{})); n != 3 {
		t.Fatalf("expected 3 events for s1, got %d", n)
	}
	// No filter returns everything.
	if n := len(filterAuditEvents(events, "", "", time.Time{})); n != 4 {
		t.Fatalf("expected 4 events unfiltered, got %d", n)
	}
}

func TestFilterAuditEvents_Since(t *testing.T) {
	now := time.Now()
	events := []audit.Event{
		{Ts: now.Add(-2 * time.Hour), Kind: audit.KindTurnStart, Session: "s", Run: "old", Seq: 1},
		{Ts: now.Add(-10 * time.Minute), Kind: audit.KindTurnStart, Session: "s", Run: "new", Seq: 1},
	}
	got := filterAuditEvents(events, "", "", now.Add(-1*time.Hour))
	if len(got) != 1 || got[0].Run != "new" {
		t.Fatalf("since filter wrong: %+v", got)
	}
}

func TestLoadAuditEvents_LiveAndRotated(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "agent-audit.jsonl")
	// Rotated file (.1) holds an older record; live file holds a newer one.
	if err := os.WriteFile(base+".1", []byte(`{"ts":"2026-05-27T09:00:00Z","kind":"turn_start","session":"s","run":"r0","seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte(`{"ts":"2026-05-27T10:00:00Z","kind":"turn_start","session":"s","run":"r1","seq":1}`+"\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAuditEvents(base)
	if err != nil {
		t.Fatal(err)
	}
	// Two valid records (the malformed line is skipped) across both files.
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(got), got)
	}
}

func TestRenderAuditEvents_Formats(t *testing.T) {
	events := []audit.Event{
		{Ts: time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC), Kind: audit.KindToolCall, Session: "s", Run: "r", Seq: 1, Tool: "bash"},
		{Ts: time.Date(2026, 5, 27, 10, 0, 1, 0, time.UTC), Kind: audit.KindToolResult, Session: "s", Run: "r", Seq: 2, Tool: "bash", IsError: true},
	}
	var buf bytes.Buffer
	if err := renderAuditEvents(&buf, events, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "tool_call") || !strings.Contains(out, "bash") {
		t.Fatalf("expected tool_call/bash in output:\n%s", out)
	}
	if !strings.Contains(out, "[error]") {
		t.Fatalf("expected [error] marker for errored result:\n%s", out)
	}
	if !strings.Contains(out, "s/r#1") {
		t.Fatalf("expected correlation suffix s/r#1:\n%s", out)
	}

	var jbuf bytes.Buffer
	if err := renderAuditEvents(&jbuf, events, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jbuf.String(), `"kind":"tool_call"`) {
		t.Fatalf("json output missing record:\n%s", jbuf.String())
	}
}
