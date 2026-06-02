package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

func TestConfigureToolgate_WritesMode(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TALON_STATE_DIR", stateDir)

	// Scripted stdin: choose option 2 (audit).
	in := strings.NewReader("2\n")
	out := &bytes.Buffer{}
	if err := configureToolgate(in, out); err != nil {
		t.Fatalf("configureToolgate: %v", err)
	}

	merged, err := config.MergedBytes(resolvePaths())
	if err != nil {
		t.Fatalf("MergedBytes: %v", err)
	}
	if got := gjson.GetBytes(merged, "toolgate.mode").Str; got != "audit" {
		t.Fatalf("toolgate.mode = %q, want audit", got)
	}
	if !strings.Contains(out.String(), "audit") {
		t.Errorf("wizard output should confirm the chosen mode:\n%s", out.String())
	}
}

func TestConfigureToolgate_DefaultKeepsEnforce(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TALON_STATE_DIR", stateDir)

	// Empty input (just newline) keeps the default/current mode (enforce).
	in := strings.NewReader("\n")
	out := &bytes.Buffer{}
	if err := configureToolgate(in, out); err != nil {
		t.Fatalf("configureToolgate: %v", err)
	}
	merged, err := config.MergedBytes(resolvePaths())
	if err != nil {
		t.Fatalf("MergedBytes: %v", err)
	}
	// Either unset (defaults to enforce) or explicitly enforce — never audit/off.
	if got := gjson.GetBytes(merged, "toolgate.mode").Str; got != "" && got != "enforce" {
		t.Fatalf("default selection should leave enforce, got %q", got)
	}
	_ = talonpath.Paths{}
}
