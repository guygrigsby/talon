package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsFresh(t *testing.T) {
	dir := t.TempDir()
	if !IsFresh(dir) {
		t.Fatal("empty dir should be fresh")
	}
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsFresh(dir) {
		t.Error("dir with a persona file is not fresh")
	}
	if IsFresh("") {
		t.Error("empty path is not fresh")
	}
}

func TestEnsureBootstrap_WritesOnceAndActivates(t *testing.T) {
	dir := t.TempDir()

	if BootstrapActive(dir) {
		t.Fatal("no sentinel yet")
	}
	wrote, err := EnsureBootstrap(dir)
	if err != nil || !wrote {
		t.Fatalf("EnsureBootstrap: wrote=%v err=%v", wrote, err)
	}
	if !BootstrapActive(dir) {
		t.Fatal("sentinel should be active after write")
	}

	// Second call is a no-op (don't clobber / re-arm).
	wrote2, err := EnsureBootstrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if wrote2 {
		t.Error("EnsureBootstrap should not rewrite an existing sentinel")
	}
}

func TestBootstrapPrompt_LeadsWithDirectiveWhenActive(t *testing.T) {
	dir := t.TempDir()
	if got := BootstrapPrompt(dir); got != "" {
		t.Errorf("inactive onboarding should yield empty prompt, got %q", got)
	}
	if _, err := EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	got := BootstrapPrompt(dir)
	if !strings.Contains(got, "finish_onboarding") {
		t.Errorf("directive should name the tool; got:\n%s", got)
	}
	if !strings.Contains(got, "interview") {
		t.Errorf("directive should instruct an interview; got:\n%s", got)
	}
}

func TestApplyOnboarding_WritesFilesAndClearsSentinel(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}

	fields := PersonaFields{
		AgentName:    "Cawdia",
		Creature:     "automation raven",
		Vibe:         "gets shit done, practical",
		Emoji:        "🐦‍⬛",
		UserName:     "Guy",
		UserCall:     "human",
		UserTimezone: "America/Denver",
		UserNotes:    "Direct, gets-it-done. Uses 1Password.",
	}
	if err := ApplyOnboarding(dir, fields); err != nil {
		t.Fatalf("ApplyOnboarding: %v", err)
	}

	// Sentinel gone → onboarding no longer active.
	if BootstrapActive(dir) {
		t.Error("BOOTSTRAP.md should be deleted after onboarding")
	}

	id, err := os.ReadFile(filepath.Join(dir, "IDENTITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cawdia", "automation raven", "🐦‍⬛"} {
		if !strings.Contains(string(id), want) {
			t.Errorf("IDENTITY.md missing %q:\n%s", want, id)
		}
	}
	usr, err := os.ReadFile(filepath.Join(dir, "USER.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Guy", "America/Denver", "1Password"} {
		if !strings.Contains(string(usr), want) {
			t.Errorf("USER.md missing %q:\n%s", want, usr)
		}
	}
}

func TestApplyOnboarding_RequiresAgentName(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBootstrap(dir); err != nil {
		t.Fatal(err)
	}
	err := ApplyOnboarding(dir, PersonaFields{UserName: "Guy"})
	if err == nil {
		t.Fatal("expected error when agent name is empty")
	}
	// Sentinel must survive a failed onboarding so the agent can retry.
	if !BootstrapActive(dir) {
		t.Error("failed onboarding should not clear the sentinel")
	}
}
