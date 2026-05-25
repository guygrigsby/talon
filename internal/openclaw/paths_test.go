package openclaw

import (
	"path/filepath"
	"testing"
)

func TestDefaultPaths_Defaults(t *testing.T) {
	t.Setenv("TALON_STATE_DIR", "")
	t.Setenv("OPENCLAW_STATE_DIR", "")
	t.Setenv("TALON_CONFIG_PATH", "")
	t.Setenv("OPENCLAW_CONFIG_PATH", "")
	t.Setenv("HOME", "/tmp/home-fixture")
	p := DefaultPaths()
	if want := "/tmp/home-fixture/.talon"; p.Talon.Dir != want {
		t.Errorf("talon dir = %q, want %q", p.Talon.Dir, want)
	}
	if want := "/tmp/home-fixture/.openclaw"; p.Openclaw.Dir != want {
		t.Errorf("openclaw dir = %q, want %q", p.Openclaw.Dir, want)
	}
	if want := "/tmp/home-fixture/.talon/talon.json"; p.Talon.Config != want {
		t.Errorf("talon config = %q, want %q", p.Talon.Config, want)
	}
}

func TestDefaultPaths_EnvOverrides(t *testing.T) {
	t.Setenv("TALON_STATE_DIR", "/custom/talon")
	t.Setenv("OPENCLAW_STATE_DIR", "/custom/oc")
	t.Setenv("TALON_CONFIG_PATH", "")
	t.Setenv("OPENCLAW_CONFIG_PATH", "/etc/openclaw.json")
	p := DefaultPaths()
	if p.Talon.Dir != "/custom/talon" {
		t.Errorf("talon dir = %q", p.Talon.Dir)
	}
	if p.Talon.Config != "/custom/talon/talon.json" {
		t.Errorf("talon config = %q", p.Talon.Config)
	}
	if p.Openclaw.Config != "/etc/openclaw.json" {
		t.Errorf("openclaw config = %q (env should win over state-dir-derived path)", p.Openclaw.Config)
	}
}

func TestLayerPaths(t *testing.T) {
	l := Layer{Dir: "/state", Config: "/state/openclaw.json"}
	cases := map[string]string{
		l.ConfigBackupPath(0):     "/state/openclaw.json.bak",
		l.ConfigBackupPath(3):     "/state/openclaw.json.bak.3",
		l.LastGoodPath():          "/state/openclaw.json.last-good",
		l.LogsDir():               "/state/logs",
		l.ConfigAuditLogPath():    "/state/logs/config-audit.jsonl",
		l.CredentialsDir():        "/state/credentials",
		l.AgentDir("coding"):      "/state/agents/coding",
	}
	for got, want := range cases {
		if filepath.ToSlash(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/h")
	cases := map[string]string{
		"~":         "/h",
		"~/foo":     "/h/foo",
		"~/foo/bar": "/h/foo/bar",
		"/abs":      "/abs",
		"rel":       "rel",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
