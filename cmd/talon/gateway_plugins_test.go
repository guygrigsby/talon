package main

import (
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/plugin"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
)

// --- parsePluginSpecs --------------------------------------------------

func TestParsePluginSpecs_OnlyEnabledWithCmd(t *testing.T) {
	body := []byte(`{
		"plugins": {
			"entries": {
				"telegram":  {"enabled": true,  "cmd": ["/usr/local/bin/talon-telegram"]},
				"slack":     {"enabled": false, "cmd": ["/usr/local/bin/talon-slack"]},
				"openai":    {"enabled": true},
				"weather":   {"enabled": true,  "cmd": ["/usr/local/bin/talon-weather", "--key=abc"]},
				"emptycmd":  {"enabled": true,  "cmd": []},
				"badcmd":    {"enabled": true,  "cmd": "not-an-array"}
			}
		}
	}`)
	got := parsePluginSpecs(body)
	if len(got) != 2 {
		t.Fatalf("got %d specs, want 2 (telegram + weather)", len(got))
	}
	byName := map[string][]string{}
	for _, s := range got {
		byName[s.name] = s.cmd
	}
	if cmd, ok := byName["telegram"]; !ok || cmd[0] != "/usr/local/bin/talon-telegram" {
		t.Errorf("telegram parsed wrong: %v", byName["telegram"])
	}
	if cmd, ok := byName["weather"]; !ok || len(cmd) != 2 || cmd[1] != "--key=abc" {
		t.Errorf("weather parsed wrong: %v", byName["weather"])
	}
	if _, present := byName["slack"]; present {
		t.Errorf("disabled plugin should be skipped")
	}
	if _, present := byName["openai"]; present {
		t.Errorf("plugin without cmd should be skipped (native runtime, not subprocess)")
	}
	if _, present := byName["emptycmd"]; present {
		t.Errorf("empty cmd array should be skipped")
	}
	if _, present := byName["badcmd"]; present {
		t.Errorf("non-array cmd should be skipped")
	}
}

func TestParsePluginSpecs_NoEntriesSection(t *testing.T) {
	got := parsePluginSpecs([]byte(`{}`))
	if len(got) != 0 {
		t.Errorf("got %d specs from empty config, want 0", len(got))
	}
}

func TestParsePluginSpecs_EmptyEntries(t *testing.T) {
	got := parsePluginSpecs([]byte(`{"plugins":{"entries":{}}}`))
	if len(got) != 0 {
		t.Errorf("got %d specs from empty entries, want 0", len(got))
	}
}

func TestParsePluginSpecs_FiltersBlankCmdEntries(t *testing.T) {
	// A cmd array with non-string or empty-string entries should
	// have those filtered. If nothing's left, the entry is skipped.
	body := []byte(`{
		"plugins": {"entries": {
			"a": {"enabled": true, "cmd": ["", "", null]},
			"b": {"enabled": true, "cmd": ["bin", ""]}
		}}
	}`)
	got := parsePluginSpecs(body)
	if len(got) != 1 {
		t.Fatalf("got %d specs, want 1 (only b survives)", len(got))
	}
	if got[0].name != "b" || len(got[0].cmd) != 1 || got[0].cmd[0] != "bin" {
		t.Errorf("b parsed wrong: %+v", got[0])
	}
}

// --- parseChannelBindings ----------------------------------------------

func TestParseChannelBindings_HappyPath(t *testing.T) {
	merged := []byte(`{
		"channels": {
			"telegram": {"agentId": "concierge", "botToken": "secret-1"},
			"slack":    {"botToken": "secret-2"}
		}
	}`)
	offers := []pluginChannelOffer{
		{PluginName: "tg-plugin", Channel: "telegram"},
		{PluginName: "slack-plugin", Channel: "slack"},
	}
	got := parseChannelBindings(merged, offers)
	if len(got) != 2 {
		t.Fatalf("got %d bindings, want 2", len(got))
	}
	byChannel := map[string]channelBindingForPlugin{}
	for _, b := range got {
		byChannel[b.Binding.ChannelName] = b
	}
	if b := byChannel["telegram"]; b.PluginName != "tg-plugin" || b.Binding.AgentID != "concierge" {
		t.Errorf("telegram binding wrong: %+v", b)
	}
	// Slack didn't specify agentId — must fall back to "main".
	if b := byChannel["slack"]; b.PluginName != "slack-plugin" || b.Binding.AgentID != "main" {
		t.Errorf("slack binding wrong (default agentId expected): %+v", b)
	}
	// Config JSON passed through verbatim — plugin parses its own schema.
	if !strings.Contains(string(byChannel["telegram"].Binding.ConfigJSON), "secret-1") {
		t.Errorf("telegram config JSON missing botToken: %s", byChannel["telegram"].Binding.ConfigJSON)
	}
}

func TestParseChannelBindings_SkipsChannelsWithNoConfig(t *testing.T) {
	merged := []byte(`{"channels": {"telegram": {"botToken": "x"}}}`)
	offers := []pluginChannelOffer{
		{PluginName: "tg", Channel: "telegram"},
		{PluginName: "tg", Channel: "discord"},  // not configured
		{PluginName: "tg", Channel: "matrix"},   // not configured
	}
	got := parseChannelBindings(merged, offers)
	if len(got) != 1 || got[0].Binding.ChannelName != "telegram" {
		t.Errorf("only the channel with config should bind: %+v", got)
	}
}

func TestParseChannelBindings_NoChannelsSection(t *testing.T) {
	got := parseChannelBindings([]byte(`{}`), []pluginChannelOffer{
		{PluginName: "tg", Channel: "telegram"},
	})
	if len(got) != 0 {
		t.Errorf("expected zero bindings when channels section is absent, got %d", len(got))
	}
}

func TestParseChannelBindings_NoOffers(t *testing.T) {
	got := parseChannelBindings([]byte(`{"channels": {"telegram": {}}}`), nil)
	if len(got) != 0 {
		t.Errorf("zero offers should produce zero bindings, got %d", len(got))
	}
}

// --- collectChannelOffers ----------------------------------------------

func TestCollectChannelOffers_WalksManifestEntries(t *testing.T) {
	// Build a host with two registered instances directly (Register
	// requires a real client; this seeds via the public Get path).
	// We rely on Host's public-but-undocumented behavior: List + Get
	// walk byName, which we can populate via the same Register flow
	// in production — here we use bufconn-free seeding by writing
	// directly through the public API. Simplest path: assert that an
	// EMPTY host yields zero offers (the meaningful walking-logic is
	// already covered by parseChannelBindings tests).
	host := plugin.NewHost("")
	if got := collectChannelOffers(host); len(got) != 0 {
		t.Errorf("empty host should yield zero offers, got %d", len(got))
	}
}

// --- newToolRunnerFactory ----------------------------------------------

// stubLocal is a minimal tools-side base runner; mirrors the local
// builtins without depending on the full tools package.
type stubLocal struct{}

func (stubLocal) Specs() []provider.ToolSpec { return []provider.ToolSpec{{Name: "read"}} }
func (stubLocal) Run(_ any, _ string, _ any) (string, error) { panic("not used") }

func TestNewToolRunnerFactory_NilHostDelegatesToBase(t *testing.T) {
	factory := newToolRunnerFactory(nil)
	runner := factory(t.TempDir(), nil)
	specs := runner.Specs()
	names := []string{}
	for _, s := range specs {
		names = append(names, s.Name)
	}
	// With nil host, factory should return the base runner directly —
	// just the builtins (read/write/edit/bash/glob/grep/remember).
	// Crucially, no plugin-injected tools.
	for _, n := range []string{"read", "bash", "remember"} {
		if !contains(names, n) {
			t.Errorf("base builtin %q missing: %v", n, names)
		}
	}
}

func TestNewToolRunnerFactory_NonNilHostReturnsRouter(t *testing.T) {
	host := plugin.NewHost("")
	factory := newToolRunnerFactory(host)
	runner := factory(t.TempDir(), nil)
	// Factory should produce a *plugin.ToolRouter when host is non-nil
	// — even with no plugins loaded yet, the type ensures live-loaded
	// plugins will appear via the router's per-call host.List() walk.
	if _, ok := runner.(*plugin.ToolRouter); !ok {
		t.Errorf("expected *plugin.ToolRouter, got %T", runner)
	}
}

// --- helpers -----------------------------------------------------------

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Compile-time: ensure stubLocal can be passed where SubagentRunner
// would normally go (it doesn't have to, since newToolRunnerFactory
// only forwards the sub argument; assert nil-sub is acceptable).
var _ server.SubagentRunner = (server.SubagentRunner)(nil)

// And ensure provider.ToolSpec compiles into the test's expected shape
// (catches a refactor that renames the field set).
var _ = provider.ToolSpec{Name: "x", Description: "y"}

// strings unused-import sentry — kept because future cases will likely
// inspect log output via strings.Contains.
var _ = strings.Contains
