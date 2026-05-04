package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// fakeWizard records that it ran and what input it saw. Used by
// runConfigureMenu_RunsSelected to assert the right entry fired.
func fakeWizard(name string, ran *[]string) func(io.Reader, io.Writer) error {
	return func(_ io.Reader, _ io.Writer) error {
		*ran = append(*ran, name)
		return nil
	}
}

// patchWizards replaces configureWizards for the duration of one test.
// Restores via t.Cleanup so test order doesn't matter.
func patchWizards(t *testing.T, list []configureWizard) {
	t.Helper()
	prev := configureWizardsForTest
	configureWizardsForTest = list
	t.Cleanup(func() { configureWizardsForTest = prev })
}

func TestRunConfigureMenu_QuitsImmediately(t *testing.T) {
	out := &bytes.Buffer{}
	if err := runConfigureMenu(strings.NewReader("q\n"), out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "talon configure") {
		t.Errorf("menu should have rendered; got %q", out.String())
	}
}

func TestRunConfigureMenu_EOFExits(t *testing.T) {
	// Stdin closed without input — runConfigureMenu must exit
	// cleanly so non-interactive callers (e.g. piped scripts) don't
	// hang or loop.
	if err := runConfigureMenu(strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestRunConfigureMenu_RunsSelectedThenLoops(t *testing.T) {
	var ran []string
	patchWizards(t, []configureWizard{
		{Label: "First", Run: fakeWizard("first", &ran)},
		{Label: "Second", Run: fakeWizard("second", &ran)},
	})
	// Pick 2 → wizard runs → menu reprints → q quits.
	in := strings.NewReader("2\nq\n")
	if err := runConfigureMenu(in, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "second" {
		t.Errorf("ran = %v, want [second]", ran)
	}
}

func TestRunConfigureMenu_InvalidSelectionRePrompts(t *testing.T) {
	var ran []string
	patchWizards(t, []configureWizard{
		{Label: "Only", Run: fakeWizard("only", &ran)},
	})
	out := &bytes.Buffer{}
	// 99 is out of range → reprint with error → 1 picks Only → q quits.
	in := strings.NewReader("99\n1\nq\n")
	if err := runConfigureMenu(in, out); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "only" {
		t.Errorf("ran = %v, want [only]", ran)
	}
	if !strings.Contains(out.String(), "invalid selection") {
		t.Errorf("expected invalid-selection message; got %q", out.String())
	}
}

func TestRunConfigureMenu_BlankSelectionSkips(t *testing.T) {
	var ran []string
	patchWizards(t, []configureWizard{
		{Label: "X", Run: fakeWizard("x", &ran)},
	})
	in := strings.NewReader("\n\nq\n")
	if err := runConfigureMenu(in, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("blank input should not run a wizard; ran = %v", ran)
	}
}

func TestRunConfigureMenu_WizardErrorContinuesMenu(t *testing.T) {
	// A failing wizard should surface its error and return the user
	// to the menu, not exit. Simulates "user typed bad input mid-
	// wizard, fix it, pick again."
	failing := configureWizard{
		Label: "Boom",
		Run:   func(io.Reader, io.Writer) error { return fmt.Errorf("kapow") },
	}
	patchWizards(t, []configureWizard{failing})
	out := &bytes.Buffer{}
	in := strings.NewReader("1\nq\n")
	if err := runConfigureMenu(in, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wizard failed: kapow") {
		t.Errorf("expected error surface; got %q", out.String())
	}
}

func TestFindWizard(t *testing.T) {
	patchWizards(t, []configureWizard{
		{Kind: "channel", Name: "telegram", Label: "Telegram"},
		{Kind: "channel", Name: "bluebubbles", Aliases: []string{"bb", "imessage"}, Label: "BlueBubbles"},
		{Kind: "provider", Name: "openai", Label: "OpenAI"},
	})
	cases := []struct {
		kind, name string
		wantName   string
		wantOK     bool
	}{
		{"channel", "telegram", "telegram", true},
		{"channel", "TELEGRAM", "telegram", true}, // case-insensitive
		{"channel", "bb", "bluebubbles", true},    // alias
		{"channel", "imessage", "bluebubbles", true},
		{"channel", "openai", "", false},  // wrong kind
		{"provider", "openai", "openai", true},
		{"channel", "missing", "", false},
	}
	for _, tc := range cases {
		got, ok := findWizard(tc.kind, tc.name)
		if ok != tc.wantOK {
			t.Errorf("findWizard(%q,%q) ok=%v, want %v", tc.kind, tc.name, ok, tc.wantOK)
			continue
		}
		if ok && got.Name != tc.wantName {
			t.Errorf("findWizard(%q,%q).Name=%q, want %q", tc.kind, tc.name, got.Name, tc.wantName)
		}
	}
}

func TestWizardsByKind(t *testing.T) {
	patchWizards(t, []configureWizard{
		{Kind: "channel", Name: "a"},
		{Kind: "provider", Name: "b"},
		{Kind: "channel", Name: "c"},
	})
	got := wizardsByKind("channel")
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("got %+v, want [a c]", got)
	}
	if len(wizardsByKind("provider")) != 1 {
		t.Errorf("provider count wrong")
	}
	if len(wizardsByKind("nonexistent")) != 0 {
		t.Errorf("unknown kind should return empty")
	}
}

func TestRunWizardSubmenu_PicksByIndex(t *testing.T) {
	var ran []string
	wizards := []configureWizard{
		{Kind: "channel", Name: "first", Label: "First", Run: fakeWizard("first", &ran)},
		{Kind: "channel", Name: "second", Label: "Second", Run: fakeWizard("second", &ran)},
	}
	out := &bytes.Buffer{}
	in := strings.NewReader("2\nq\n")
	if err := runWizardSubmenu(in, out, "channel", wizards); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "second" {
		t.Errorf("ran = %v", ran)
	}
	if !strings.Contains(out.String(), "talon configure channel") {
		t.Errorf("submenu header missing; got %q", out.String())
	}
}

func TestRunWizardSubmenu_EmptyListErrors(t *testing.T) {
	if err := runWizardSubmenu(strings.NewReader(""), io.Discard, "channel", nil); err == nil {
		t.Error("empty wizard list should error")
	}
}
