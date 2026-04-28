//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_MultiPluginLoading boots a gateway with two plugins
// configured: the bundled Go testplugin AND the openclaw shim wrapping
// the fake-tool fixture. Both should load and announce in any order.
//
// What this guards: the host's per-plugin spawn loop is independent —
// one plugin's slow Initialize must not gate the other's load, and
// shared registries (byName/byCookie) must tolerate concurrent
// registrations.
func TestE2E_MultiPluginLoading(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"testplugin": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-testplugin"]
				},
				"openclaw-fake": {
					"enabled": true,
					"cmd": ["node", "/usr/local/bin/openclaw-plugin-host", "/opt/test-fixtures/openclaw-fake-tool"]
				}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	if _, err := g.WaitForLog("plugin testplugin: loaded", 30*time.Second); err != nil {
		t.Errorf("did not see testplugin load: %v\n--- logs ---\n%s", err, g.LogsString())
	}
	if _, err := g.WaitForLog("plugin openclaw-fake: loaded", 30*time.Second); err != nil {
		t.Errorf("did not see shim load: %v\n--- logs ---\n%s", err, g.LogsString())
	}

	// The "talon gateway listening" announce includes plugins=N. With
	// two configured + both loaded, we expect plugins=2.
	if _, err := g.WaitForLog("plugins=2", 5*time.Second); err != nil {
		t.Errorf("expected plugins=2 in startup announce: %v", err)
	}
}

// TestE2E_PluginCrashOnLoadDoesNotKillGateway is the crash-isolation
// load-bearing test. The user's working memory documents a real openclaw
// bug (bonjour) where an in-process plugin crash killed the whole
// gateway; the talon subprocess model is supposed to make those failures
// containable. We force a load failure by pointing cmd at a binary that
// exits before printing a handshake, and verify the gateway still
// announces "listening" plus the per-plugin "load failed" line.
func TestE2E_PluginCrashOnLoadDoesNotKillGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	// /bin/false exits 1 immediately — no handshake, no gRPC. The
	// host's readHandshake should observe stdout close + nonzero exit
	// and return an error; loadConfiguredPlugins logs and continues.
	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"crashy":     {"enabled": true, "cmd": ["/bin/false"]},
				"testplugin": {"enabled": true, "cmd": ["/usr/local/bin/talon-testplugin"]}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	// The gateway must still come up despite the crashing plugin.
	// (StartGateway already waited for "talon gateway listening" — if
	// we got the Gateway handle back, that log line fired.)
	if _, err := g.WaitForLog("plugin crashy: load failed", 30*time.Second); err != nil {
		t.Errorf("expected 'load failed' for crashy plugin: %v\n--- logs ---\n%s", err, g.LogsString())
	}

	// The healthy plugin should still load.
	if _, err := g.WaitForLog("plugin testplugin: loaded", 30*time.Second); err != nil {
		t.Errorf("healthy plugin should still load alongside crashy one: %v", err)
	}

	// Final announce reports only the healthy one.
	if _, err := g.WaitForLog("plugins=1", 5*time.Second); err != nil {
		t.Errorf("expected plugins=1 in announce (one failed, one loaded); got logs:\n%s", g.LogsString())
	}
}

// TestE2E_DisabledPluginNotSpawned: a plugin with enabled=false in
// config must not be spawned at all — no "loaded" log, no "load failed"
// log. Guards parsePluginSpecs's enabled-flag handling.
func TestE2E_DisabledPluginNotSpawned(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"disabled-one": {"enabled": false, "cmd": ["/usr/local/bin/talon-testplugin"]},
				"testplugin":   {"enabled": true,  "cmd": ["/usr/local/bin/talon-testplugin"]}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	if _, err := g.WaitForLog("plugin testplugin: loaded", 30*time.Second); err != nil {
		t.Fatalf("enabled plugin failed to load: %v", err)
	}

	// "disabled-one" must never appear in any plugin-related log line.
	logs := g.LogsString()
	if strings.Contains(logs, "disabled-one") {
		t.Errorf("disabled plugin should be silent in logs; got:\n%s", logs)
	}
	if _, err := g.WaitForLog("plugins=1", 5*time.Second); err != nil {
		t.Errorf("expected plugins=1 (the enabled one); logs:\n%s", g.LogsString())
	}
}

// TestE2E_BadPluginCmdLogsAndContinues: a plugin whose cmd[0] doesn't
// exist on disk fails fast at the exec.Command stage, before any
// handshake. The gateway must log the error and keep running.
func TestE2E_BadPluginCmdLogsAndContinues(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}

	cfg := []byte(`{
		"agents": {"list": [{"id": "main", "model": "testprov/echo-1", "workspace": "/tmp/ws"}]},
		"plugins": {
			"entries": {
				"missing-bin": {"enabled": true, "cmd": ["/does/not/exist/talon-nope"]}
			}
		},
		"gateway": {"auth": {"mode": "none"}}
	}`)

	g := StartGateway(t, GatewayOpts{ConfigJSON: cfg, StartupTimeout: 90 * time.Second})

	if _, err := g.WaitForLog("plugin missing-bin: load failed", 15*time.Second); err != nil {
		t.Errorf("expected load-failed log for missing binary: %v\n--- logs ---\n%s", err, g.LogsString())
	}
	if _, err := g.WaitForLog("plugins=0", 5*time.Second); err != nil {
		t.Errorf("expected plugins=0 in announce; logs:\n%s", g.LogsString())
	}
}
