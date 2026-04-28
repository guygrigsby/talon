//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_OpenclawShim_LoadsExtension is the load-bearing Phase-1 check
// for the openclaw Node.js compat shim. It boots a gateway that
// spawns openclaw-plugin-host as a subprocess pointed at the
// fake-tool fixture, and verifies the gateway's "plugin loaded" log
// line announces a tool the fixture registered via the openclaw
// register(api) flow.
//
// What this proves:
//   - shim refuses to start without the handshake env (covered by
//     spawn.go on the talon side; the gateway sets the env)
//   - shim's gRPC server binds and prints a parseable handshake line
//   - extension loader resolves a directory entry via package.json's
//     openclaw.extensions[]
//   - api.registerTool capture survives the round trip into
//     Initialize's manifest
//
// What this doesn't yet prove (Phase 2):
//   - RunTool actually dispatches and the tool's execute() runs
//   - registerHttpRoute / registerProvider / registerWebSearchProvider
//     bridges
func TestE2E_OpenclawShim_LoadsExtension(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "openai/gpt-4o", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"openclaw-fake": {
					"enabled": true,
					"cmd": ["node", "/usr/local/bin/openclaw-plugin-host", "/opt/test-fixtures/openclaw-fake-tool"]
				}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	line, err := g.WaitForLog("plugin openclaw-fake: loaded", 30*time.Second)
	if err != nil {
		t.Fatalf("did not see shim load announce: %v\n--- container logs ---\n%s", err, g.LogsString())
	}
	// Manifest counts: tools=1, providers=0, channels=0 — the fixture
	// only calls registerTool. registerHttpRoute is captured-but-ignored
	// at the shim level so it doesn't show in the manifest.
	if !strings.Contains(line, "tools=1") {
		t.Errorf("expected tools=1 in manifest announce, got %q", line)
	}
	if !strings.Contains(line, "providers=0") || !strings.Contains(line, "channels=0") {
		t.Errorf("unexpected non-tool offers in manifest: %q", line)
	}

	// The fixture's registerHttpRoute call should have produced a
	// shim-side warning — confirms the captured-but-ignored path works.
	if !strings.Contains(g.LogsString(), "registerHttpRoute(): not yet bridged") {
		t.Errorf("expected shim warning for unsupported registerHttpRoute; got logs:\n%s", g.LogsString())
	}
}
