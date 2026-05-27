package config

import "testing"

func TestClassifyReload(t *testing.T) {
	cases := []struct {
		path string
		want ReloadClass
	}{
		// Restart-required.
		{"gateway.port", ReloadRestart},
		{"gateway.bind", ReloadRestart},
		{"gateway.auth.mode", ReloadRestart},
		{"gateway.auth.token", ReloadRestart},
		{"gateway.auth.password", ReloadRestart},
		{"gateway.tailscale.mode", ReloadRestart},
		{"gateway.controlUi.root", ReloadRestart},
		{"plugins.entries.brave.enabled", ReloadRestart},
		{"plugins.entries.openai.enabled", ReloadRestart},
		{"plugins.deny", ReloadRestart},
		{"plugins.load.paths", ReloadRestart},
		{"skills.install.nodeManager", ReloadRestart},
		{"skills.entries.openai-whisper-api.apiKey", ReloadRestart},
		{"memory.enabled", ReloadRestart},
		{"memory.recall.min_score", ReloadRestart},

		// Next-RPC (default).
		{"agents.list", ReloadNextRPC},
		{"agents.list.0.model", ReloadNextRPC},
		{"agents.defaults.workspace", ReloadNextRPC},
		{"models.providers.deepseek.api", ReloadNextRPC},
		{"channels.telegram.botToken", ReloadNextRPC},
		{"channels.telegram.groups.*.requireMention", ReloadNextRPC},
		{"auth.profiles.openai:default.mode", ReloadNextRPC},
		{"hooks.internal.entries.boot-md.enabled", ReloadNextRPC},
		{"session.dmScope", ReloadNextRPC},
		{"meta.lastTouchedAt", ReloadNextRPC},
		{"plugins.entries.brave.config.webSearch.apiKey", ReloadNextRPC},
		{"unknownTopLevel", ReloadNextRPC},
		{"", ReloadNextRPC},
	}
	for _, tc := range cases {
		segs, _ := ParsePath(tc.path)
		if got := ClassifyReload(segs); got != tc.want {
			t.Errorf("ClassifyReload(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParseReloadClass(t *testing.T) {
	cases := []struct {
		in    string
		class ReloadClass
		ok    bool
	}{
		{"", 0, false},
		{"next-rpc", ReloadNextRPC, true},
		{"NEXTRPC", ReloadNextRPC, true},
		{"hup", ReloadHUP, true},
		{"SIGHUP", ReloadHUP, true},
		{"restart", ReloadRestart, true},
		{"  restart  ", ReloadRestart, true},
		{"unknown", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseReloadClass(tc.in)
		if ok != tc.ok || (ok && got != tc.class) {
			t.Errorf("ParseReloadClass(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.class, tc.ok)
		}
	}
}

func TestReloadClass_String(t *testing.T) {
	cases := map[ReloadClass]string{
		ReloadNextRPC: "next-rpc",
		ReloadHUP:     "hup",
		ReloadRestart: "restart",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("ReloadClass(%d).String() = %q, want %q", c, got, want)
		}
	}
}

func TestReloadClass_Hint(t *testing.T) {
	if got := ReloadRestart.Hint("gateway.port"); got == "" || !contains(got, "restart") {
		t.Errorf("restart hint should mention 'restart': %q", got)
	}
	if got := ReloadHUP.Hint("foo"); got == "" || !contains(got, "SIGHUP") {
		t.Errorf("hup hint should mention 'SIGHUP': %q", got)
	}
	if got := ReloadNextRPC.Hint("agents.list"); got == "" || !contains(got, "no restart") {
		t.Errorf("next-rpc hint must say 'no restart': %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
