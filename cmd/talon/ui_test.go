package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildUIURL_DefaultsAreCorrect(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/chat")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL produced: %v\n%s", err, got)
	}
	if parsed.Host != "localhost:5173" {
		t.Errorf("host = %q, want localhost:5173", parsed.Host)
	}
	if parsed.Path != "/chat" {
		t.Errorf("path = %q, want /chat", parsed.Path)
	}
	frag, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("fragment not query-shaped: %v", err)
	}
	if frag.Get("gatewayUrl") != "ws://localhost:18790" {
		t.Errorf("gatewayUrl fragment = %q, want ws://localhost:18790", frag.Get("gatewayUrl"))
	}
	if frag.Has("token") {
		t.Errorf("token should be absent when not provided: %q", frag.Get("token"))
	}
}

func TestBuildUIURL_TokenAndSession(t *testing.T) {
	got := buildUIURL("http://example.test", "127.0.0.1", 9000, "secret-tok", "agent:main:main", "/chat")
	parsed, _ := url.Parse(got)
	// session in query, percent-encoded.
	if parsed.Query().Get("session") != "agent:main:main" {
		t.Errorf("session query = %q, want agent:main:main", parsed.Query().Get("session"))
	}
	frag, _ := url.ParseQuery(parsed.Fragment)
	if frag.Get("token") != "secret-tok" {
		t.Errorf("token fragment = %q, want secret-tok", frag.Get("token"))
	}
	if frag.Get("gatewayUrl") != "ws://127.0.0.1:9000" {
		t.Errorf("gatewayUrl fragment = %q", frag.Get("gatewayUrl"))
	}
	// Token must NOT appear in the query portion.
	if strings.Contains(parsed.RawQuery, "token") {
		t.Errorf("token leaked into query: %s", parsed.RawQuery)
	}
}

func TestBuildUIURL_NoSessionMeansNoQuery(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/chat")
	parsed, _ := url.Parse(got)
	if parsed.RawQuery != "" {
		t.Errorf("expected empty query when no session: %q", parsed.RawQuery)
	}
}

func TestBuildUIURL_CustomPath(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18790, "", "", "/agents")
	parsed, _ := url.Parse(got)
	if parsed.Path != "/agents" {
		t.Errorf("path = %q, want /agents", parsed.Path)
	}
}
