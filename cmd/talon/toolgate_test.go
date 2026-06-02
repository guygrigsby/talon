package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/audit"
)

func TestRenderToolgate_ShowsModeGrantAndVerdicts(t *testing.T) {
	merged := []byte(`{
		"toolgate": {"mode": "audit"},
		"agents": {
			"defaults": {"workspace": "/work"},
			"list": [{"id": "main", "toolgate": {"allow": ["exec"]}}]
		}
	}`)
	events := []audit.Event{
		{Kind: audit.KindToolGate, Agent: "main", Tool: "bash", Verdict: "needs-approval", Text: "ungranted capability exec (unscoped)", Ts: time.Now()},
		{Kind: audit.KindToolCall, Agent: "main", Tool: "read"},                    // ignored (not a gate event)
		{Kind: audit.KindToolGate, Agent: "other", Tool: "write", Verdict: "deny"}, // ignored (other agent)
	}
	var buf bytes.Buffer
	if err := renderToolgate(&buf, merged, "main", events); err != nil {
		t.Fatalf("renderToolgate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"main", "audit", "/work", "exec", "bash", "needs-approval"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The other agent's verdict (agent "other", verdict "deny") must not leak.
	if strings.Contains(out, "other") || strings.Contains(out, "deny") {
		t.Errorf("output leaked another agent's verdict:\n%s", out)
	}
}

func TestRenderToolgate_DefaultModeEnforce(t *testing.T) {
	merged := []byte(`{"agents": {"list": [{"id": "main", "workspace": "/ws"}]}}`)
	var buf bytes.Buffer
	if err := renderToolgate(&buf, merged, "main", nil); err != nil {
		t.Fatalf("renderToolgate: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "enforce") {
		t.Errorf("unset mode should render as enforce:\n%s", out)
	}
	// Default grant: fs.read + fs.write scoped to the workspace.
	if !strings.Contains(out, "fs.read") || !strings.Contains(out, "fs.write") {
		t.Errorf("default grant should list fs.read + fs.write:\n%s", out)
	}
}
