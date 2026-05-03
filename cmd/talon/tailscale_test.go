package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTailscaleMode(t *testing.T) {
	cases := []struct {
		in      string
		want    tailscaleMode
		wantErr bool
	}{
		{"", tailscaleOff, false},
		{"off", tailscaleOff, false},
		{"OFF", tailscaleOff, false},
		{"  off  ", tailscaleOff, false},
		{"serve", tailscaleServe, false},
		{"Serve", tailscaleServe, false},
		{"funnel", tailscaleFunnel, false},
		{"on", "", true}, // not a real mode
		{"public", "", true},
		{"http", "", true},
	}
	for _, c := range cases {
		got, err := parseTailscaleMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTailscaleMode(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTailscaleMode(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseTailscaleMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTailscaleServeArgs_Off(t *testing.T) {
	args, err := tailscaleServeArgs(tailscaleOff, 18790)
	if err != nil {
		t.Fatal(err)
	}
	if args != nil {
		t.Errorf("off mode should return nil args, got %v", args)
	}
}

func TestTailscaleServeArgs_Serve(t *testing.T) {
	args, err := tailscaleServeArgs(tailscaleServe, 18790)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"serve", "--bg", "--https=443", "http://127.0.0.1:18790"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestTailscaleServeArgs_Funnel(t *testing.T) {
	args, err := tailscaleServeArgs(tailscaleFunnel, 18790)
	if err != nil {
		t.Fatal(err)
	}
	// Funnel must include --funnel=on.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--funnel=on") {
		t.Errorf("funnel args missing --funnel=on: %v", args)
	}
	if !strings.Contains(joined, "http://127.0.0.1:18790") {
		t.Errorf("funnel args missing upstream: %v", args)
	}
}

func TestTailscaleResetArgs(t *testing.T) {
	want := []string{"serve", "--https=443", "off"}
	if !reflect.DeepEqual(tailscaleResetArgs(), want) {
		t.Errorf("resetArgs = %v, want %v", tailscaleResetArgs(), want)
	}
}
