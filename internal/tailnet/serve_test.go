package tailnet

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestServeRejectsEmptyService(t *testing.T) {
	_, err := Serve(context.Background(), Options{Hostname: "talon", StateDir: t.TempDir(), Port: 443})
	if err == nil {
		t.Fatal("want error for empty service")
	}
}

func TestServeRejectsBadServicePrefix(t *testing.T) {
	_, err := Serve(context.Background(), Options{Service: "talon", Hostname: "talon", StateDir: t.TempDir(), Port: 443})
	if err == nil || !strings.Contains(err.Error(), "svc:") {
		t.Fatalf("want svc: prefix error, got %v", err)
	}
}

func TestServeRejectsZeroPort(t *testing.T) {
	_, err := Serve(context.Background(), Options{Service: "svc:talon", Hostname: "talon", StateDir: t.TempDir(), Port: 0})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("want port error, got %v", err)
	}
}

// TestServeIntegration brings up a real tsnet node. Skipped unless an OAuth
// client secret is supplied; not run in CI/offline.
func TestServeIntegration(t *testing.T) {
	secret := os.Getenv("TALON_TEST_TS_AUTHKEY")
	if secret == "" {
		t.Skip("set TALON_TEST_TS_AUTHKEY (OAuth client secret) to run")
	}
	ln, err := Serve(context.Background(), Options{
		Hostname:      "talon-test",
		StateDir:      t.TempDir(),
		ClientSecret:  secret,
		AdvertiseTags: []string{"tag:talon"},
		Service:       "svc:talon-test",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if !strings.HasSuffix(ln.FQDN, ".ts.net") {
		t.Fatalf("FQDN = %q", ln.FQDN)
	}
}
