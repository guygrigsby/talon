//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_OpenclawShim_LoadsExtension is the load-bearing
// manifest-shape check for the openclaw Node.js compat shim. Boots a
// gateway that spawns openclaw-plugin-host as a subprocess pointed at
// the fake-tool fixture, and verifies the gateway's "plugin loaded"
// log line announces all three openclaw register*() surfaces:
// registerTool (Phase 1) + registerProvider + registerChannel
// (Phase 2).
//
// What this proves:
//   - shim refuses to start without the handshake env (covered by
//     spawn.go on the talon side; the gateway sets the env)
//   - shim's gRPC server binds and prints a parseable handshake line
//   - extension loader resolves a directory entry via package.json's
//     openclaw.extensions[]
//   - capture survives the round-trip into Initialize's manifest for
//     all three Phase-2-bridged register methods
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
	// Manifest counts: tools=1, providers=1, channels=1 — fixture
	// calls all three bridged register* methods. registerHttpRoute
	// is captured-but-ignored so it doesn't show in the manifest.
	for _, want := range []string{"tools=1", "providers=1", "channels=1"} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in manifest announce, got %q", want, line)
		}
	}

	// The fixture's registerHttpRoute call should have produced a
	// shim-side warning — confirms the captured-but-ignored path works.
	if !strings.Contains(g.LogsString(), "registerHttpRoute(): not yet bridged") {
		t.Errorf("expected shim warning for unsupported registerHttpRoute; got logs:\n%s", g.LogsString())
	}
}

// TestE2E_OpenclawShim_ChannelRoundtrip exercises the full Phase-2
// bridge surface end-to-end: the shim's StartChannel emits canned
// inbound messages, the gateway dispatcher routes each through the
// shim's StreamCompletion (the fixture provider produces "echo: " +
// the user text), and the assistant reply lands back at the shim's
// SendChannelMessage. We assert on the resulting stderr line the
// shim writes when SendChannelMessage receives a reply.
func TestE2E_OpenclawShim_ChannelRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {
			"list": [
				{"id": "main", "model": "fakeprov/echo-1", "workspace": "/tmp/ws"}
			]
		},
		"plugins": {
			"entries": {
				"openclaw-fake": {
					"enabled": true,
					"cmd": ["node", "/usr/local/bin/openclaw-plugin-host", "/opt/test-fixtures/openclaw-fake-tool"]
				}
			}
		},
		"channels": {
			"fakechan": {"agentId": "main"}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	// The fixture emits "hello" eagerly and "ping" after a 10ms tick;
	// each gets dispatched through fakeprov, which produces
	// "echo: <text>". The shim's SendChannelMessage handler doesn't
	// log on its own (we removed the openclaw-shim warning text), so
	// we assert via the gateway dispatcher's "outbound" log line.
	for _, want := range []string{"echo: hello", "echo: ping"} {
		if _, err := g.WaitForLog(want, 30*time.Second); err != nil {
			t.Errorf("did not see %q in logs: %v\n--- logs ---\n%s", want, err, g.LogsString())
		}
	}

	logs := g.LogsString()
	if !strings.Contains(logs, "channel \"fakechan\" dispatching to agent \"main\"") {
		t.Errorf("expected dispatcher startup log; got logs:\n%s", logs)
	}
}
