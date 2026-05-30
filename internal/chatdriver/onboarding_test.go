package chatdriver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/jess/tool"
	"github.com/guygrigsby/talon/internal/agentcontext"
)

// Compile-time assertion: newFinishOnboardingTool returns a tool.Tool.
var _ tool.Tool = newFinishOnboardingTool("")

func TestFinishOnboardingTool_SatisfiesToolTool(t *testing.T) {
	var _ tool.Tool = newFinishOnboardingTool(t.TempDir())
}

func TestFinishOnboardingTool_SchemaShape(t *testing.T) {
	tl := newFinishOnboardingTool(t.TempDir())
	s := tl.Schema()
	if s["type"] != "object" {
		t.Fatalf("schema type = %v, want object", s["type"])
	}
	props, _ := s["properties"].(map[string]any)
	for _, field := range []string{
		"agentName", "creature", "vibe", "emoji", "avatar",
		"userName", "userCall", "userTimezone", "userNotes",
	} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema missing property %q", field)
		}
	}
	req, _ := s["required"].([]string)
	wantReq := map[string]bool{"agentName": true}
	for _, r := range req {
		delete(wantReq, r)
	}
	if len(wantReq) > 0 {
		t.Errorf("required missing %v", wantReq)
	}
}

func TestFinishOnboardingTool_Execute(t *testing.T) {
	dir := t.TempDir()
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}

	tl := newFinishOnboardingTool(dir)
	if tl.Name() != "finish_onboarding" {
		t.Errorf("name = %q", tl.Name())
	}

	args := json.RawMessage(`{"agentName":"Cawdia","emoji":"🐦‍⬛","userName":"Guy","userTimezone":"America/Denver"}`)
	out, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(out), "onboarded") {
		t.Errorf("result should report onboarded: %s", out)
	}

	if agentcontext.BootstrapActive(dir) {
		t.Error("sentinel should be cleared after onboarding")
	}
	id, _ := os.ReadFile(filepath.Join(dir, "IDENTITY.md"))
	if !strings.Contains(string(id), "Cawdia") {
		t.Errorf("IDENTITY.md missing name:\n%s", id)
	}
	usr, _ := os.ReadFile(filepath.Join(dir, "USER.md"))
	if !strings.Contains(string(usr), "America/Denver") {
		t.Errorf("USER.md missing timezone:\n%s", usr)
	}
}

func TestFinishOnboardingTool_ExecuteWritesSentinel(t *testing.T) {
	dir := t.TempDir()
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	tl := newFinishOnboardingTool(dir)
	args, _ := json.Marshal(map[string]any{"agentName": "Test"})
	out, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) == 0 {
		t.Error("Execute returned empty JSON")
	}
	if agentcontext.BootstrapActive(dir) {
		t.Error("sentinel should be cleared after successful onboarding")
	}
}

func TestFinishOnboardingTool_RequiresName(t *testing.T) {
	dir := t.TempDir()
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	tl := newFinishOnboardingTool(dir)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"userName":"Guy"}`)); err == nil {
		t.Fatal("expected error when agentName is missing")
	}
	if !agentcontext.BootstrapActive(dir) {
		t.Error("sentinel should survive a failed onboarding")
	}
}
