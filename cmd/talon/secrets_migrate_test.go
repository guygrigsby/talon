package main

import (
	"testing"
)

func TestItemNameForPath(t *testing.T) {
	cases := map[string]string{
		"gateway.auth.token":                      "talon-gateway-auth-token",
		"channels.telegram.botToken":              "talon-channels-telegram-botToken",
		"plugins.entries.brave.config.apiKey":     "talon-plugins-entries-brave-config-apiKey",
		"agents.list[0].auth":                     "talon-agents-list-0-auth",
		`channels.["weird-name"].botToken`:        "talon-channels-weird-name-botToken",
	}
	for in, want := range cases {
		if got := itemNameForPath(in); got != want {
			t.Errorf("itemNameForPath(%q) = %q, want %q", in, got, want)
		}
	}
}
