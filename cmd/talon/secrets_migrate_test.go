package main

import (
	"testing"
)

func TestItemNameForPath(t *testing.T) {
	cases := map[string]string{
		// merged-config (dotted) paths
		"gateway.auth.token":                  "talon-gateway-auth-token",
		"channels.telegram.botToken":          "talon-channels-telegram-botToken",
		"plugins.entries.brave.config.apiKey": "talon-plugins-entries-brave-config-apiKey",
		"agents.list[0].auth":                 "talon-agents-list-0-auth",
		`channels.["weird-name"].botToken`:    "talon-channels-weird-name-botToken",
		// file:// paths (slashes + colons collapse to dashes)
		"agents/main/agent/auth-profiles.json:profiles.openai:default.key":
			"talon-agents-main-agent-auth-profiles-json-profiles-openai-default-key",
		"identity/device-auth.json:tokens.operator.token":
			"talon-identity-device-auth-json-tokens-operator-token",
	}
	for in, want := range cases {
		if got := itemNameForPath(in); got != want {
			t.Errorf("itemNameForPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFileRef(t *testing.T) {
	cases := []struct {
		in       string
		wantRel  string
		wantKey  string
		wantErr  bool
	}{
		{"file://agents/main/agent/auth-profiles.json:profiles.openai:default.key",
			"agents/main/agent/auth-profiles.json",
			"profiles.openai:default.key",
			false},
		{"file://identity/device-auth.json:tokens.operator.token",
			"identity/device-auth.json",
			"tokens.operator.token",
			false},
		// missing colon
		{"file://identity/device-auth.json", "", "", true},
		// missing key after colon
		{"file://identity/device-auth.json:", "", "", true},
		// empty rel
		{"file://:foo", "", "", true},
	}
	for _, c := range cases {
		rel, key, err := parseFileRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseFileRef(%q): expected error, got rel=%q key=%q", c.in, rel, key)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFileRef(%q): unexpected error %v", c.in, err)
			continue
		}
		if rel != c.wantRel || key != c.wantKey {
			t.Errorf("parseFileRef(%q) = (%q, %q), want (%q, %q)", c.in, rel, key, c.wantRel, c.wantKey)
		}
	}
}
