package chatdriver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/agentcontext"
)

func TestFinishOnboardingTool_Execute(t *testing.T) {
	dir := t.TempDir()
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}

	tool := newFinishOnboardingTool(dir)
	if tool.Name() != "finish_onboarding" {
		t.Errorf("name = %q", tool.Name())
	}

	args := json.RawMessage(`{"agentName":"Cawdia","emoji":"🐦‍⬛","userName":"Guy","userTimezone":"America/Denver"}`)
	out, err := tool.Execute(context.Background(), args)
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

func TestFinishOnboardingTool_RequiresName(t *testing.T) {
	dir := t.TempDir()
	if _, err := agentcontext.EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	tool := newFinishOnboardingTool(dir)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"userName":"Guy"}`)); err == nil {
		t.Fatal("expected error when agentName is missing")
	}
	if !agentcontext.BootstrapActive(dir) {
		t.Error("sentinel should survive a failed onboarding")
	}
}
