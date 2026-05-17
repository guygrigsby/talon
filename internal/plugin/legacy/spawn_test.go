package legacy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/guygrigsby/talon/internal/provider"
)

// buildTestPlugin compiles internal/plugin/testplugin into the test
// process's TempDir and returns the binary path. Cached per-test-package
// in t.TempDir() so each go-test invocation does it once.
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "talon-testplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testplugin")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build testplugin: %v", err)
	}
	return bin
}

// TestLoadPlugin_EndToEnd spawns the real test plugin binary, runs the
// handshake, dials the gRPC server, fetches the manifest, then triggers
// shutdown. Verifies the full subprocess lifecycle path that the unit
// tests skip.
func TestLoadPlugin_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped under -short")
	}
	bin := buildTestPlugin(t)

	h := NewHost("127.0.0.1:18790")
	inst, err := h.LoadPlugin(t.Context(), "testplugin", LoadOptions{
		Cmd: []string{bin},
	})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer h.Unregister("testplugin")

	if inst.Name != "testplugin" {
		t.Errorf("Name = %q, want testplugin", inst.Name)
	}
	if inst.Manifest == nil {
		t.Fatal("manifest is nil")
	}
	if inst.Manifest.Name != "testplugin" || inst.Manifest.Version != "0.1.0" {
		t.Errorf("manifest wrong: %+v", inst.Manifest)
	}
	if len(inst.Manifest.Needs) != 2 {
		t.Errorf("manifest.Needs len = %d, want 2", len(inst.Manifest.Needs))
	}

	// Cookie must be the same one the host minted (subprocess saw it via
	// env, but doesn't echo it; the host stored it on the instance).
	if len(inst.Cookie) != 48 {
		t.Errorf("cookie length = %d", len(inst.Cookie))
	}

	// Registry lookup works.
	if got := h.Get("testplugin"); got != inst {
		t.Errorf("Get returned different instance")
	}
}

func TestLoadPlugin_FailsOnMissingCmd(t *testing.T) {
	h := NewHost("")
	_, err := h.LoadPlugin(t.Context(), "x", LoadOptions{Cmd: nil})
	if err == nil || !strings.Contains(err.Error(), "empty Cmd") {
		t.Errorf("expected empty Cmd rejection, got %v", err)
	}
}

func TestLoadPlugin_FailsOnNonexistentBinary(t *testing.T) {
	h := NewHost("")
	_, err := h.LoadPlugin(t.Context(), "x", LoadOptions{
		Cmd: []string{filepath.Join(t.TempDir(), "does-not-exist")},
	})
	if err == nil {
		t.Errorf("expected error for nonexistent binary")
	}
	if !strings.Contains(err.Error(), "tried") {
		t.Errorf("error should list searched locations; got %v", err)
	}
}

// TestLoadPlugin_ProviderDispatch is the end-to-end check for the
// provider-plugin path: spawn the real testplugin subprocess, build a
// PluginProvider against its testprov offering, exercise
// StreamCompletion. Mirrors the production path agentProviderFactory
// takes when the gateway routes a model whose provider segment matches
// a loaded plugin.
func TestLoadPlugin_ProviderDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped under -short")
	}
	bin := buildTestPlugin(t)

	h := NewHost("127.0.0.1:18790")
	inst, err := h.LoadPlugin(t.Context(), "testplugin", LoadOptions{Cmd: []string{bin}})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer h.Unregister("testplugin")

	// Manifest advertises offers_providers ["testprov" → ["echo-1"]];
	// host.ProviderByName must find it.
	if got := h.ProviderByName("testprov"); got == nil || got.Name != "testplugin" {
		t.Fatalf("ProviderByName(testprov): got %+v", got)
	}

	p := NewPluginProvider("testprov", inst.Client)
	ch, err := p.Stream(t.Context(), provider.Request{
		Model:    "testprov/echo-1",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var sawUsage bool
	for d := range ch {
		switch d.Kind {
		case provider.DeltaText:
			text.WriteString(d.Text)
		case provider.DeltaUsage:
			sawUsage = true
		}
	}
	// testplugin's StreamCompletion emits "echo: " + last user message.
	if got := text.String(); got != "echo: ping" {
		t.Errorf("got %q, want %q", got, "echo: ping")
	}
	if !sawUsage {
		t.Errorf("expected a Usage delta from the plugin")
	}
}

// TestLoadPlugin_ChannelDispatch is the end-to-end check for the
// channel-plugin path: spawn the real testplugin subprocess, look up
// the testchan channel, drive a ChannelDispatcher against a recording
// runner, and verify each inbound message produced an agent run + an
// outbound SendChannelMessage. Mirrors the production wiring
// startConfiguredChannels does in cmd/talon.
func TestLoadPlugin_ChannelDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped under -short")
	}
	bin := buildTestPlugin(t)

	h := NewHost("127.0.0.1:18790")
	inst, err := h.LoadPlugin(t.Context(), "testplugin", LoadOptions{Cmd: []string{bin}})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer h.Unregister("testplugin")

	if got := h.ChannelByName("testchan"); got == nil || got.Name != "testplugin" {
		t.Fatalf("ChannelByName(testchan) = %+v", got)
	}

	runner := &recordingChannelRunner{reply: "echo-back"}
	d, err := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "testchan",
		AgentID:     "main",
		ConfigJSON:  []byte(`{}`),
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	d.Wait() // testchan emits 2 messages, then closes the stream
	d.Stop()

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("runner saw %d calls, want 2", len(calls))
	}
	// Per-message handlers run concurrently, so order isn't fixed.
	// Assert both expected sessionKeys are present + every call is for
	// the right agent.
	keys := map[string]bool{}
	for _, c := range calls {
		keys[c.sessionKey] = true
		if c.agentID != "main" {
			t.Errorf("agent id not propagated: %+v", c)
		}
	}
	if !keys["channel:testchan:room:room-1"] {
		t.Errorf("missing room-scoped key; got %+v", calls)
	}
	if !keys["channel:testchan:user:user-B"] {
		t.Errorf("missing direct-scoped key; got %+v", calls)
	}
}

// recordingChannelRunner is a SessionRunner that captures calls. Defined
// here (not in channels_test.go) because the channel test file can't
// share types across the bufconn/in-process and end-to-end paths
// without exposing internal helpers.
type recordingChannelRunner struct {
	mu    sync.Mutex
	calls []recordingChannelCall
	reply string
	err   error
}

type recordingChannelCall struct {
	sessionKey string
	agentID    string
	message    string
}

func (r *recordingChannelRunner) RunForSession(_ context.Context, sessionKey, agentID, message string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, recordingChannelCall{sessionKey, agentID, message})
	r.mu.Unlock()
	return r.reply, r.err
}

func (r *recordingChannelRunner) snapshot() []recordingChannelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordingChannelCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// TestLoadPlugin_ToolRouterDispatch is the end-to-end check for the
// tool-plugin path: spawn the real testplugin subprocess, build a
// ToolRouter that unions a local runner with the loaded plugin, then
// invoke the plugin's "test-echo" tool via the router. Verifies the
// full chat-side flow (Specs union, name-based dispatch, agent/run
// context propagation, RunTool roundtrip).
func TestLoadPlugin_ToolRouterDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped under -short")
	}
	bin := buildTestPlugin(t)

	h := NewHost("127.0.0.1:18790")
	_, err := h.LoadPlugin(t.Context(), "testplugin", LoadOptions{Cmd: []string{bin}})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer h.Unregister("testplugin")

	// Local runner advertises one builtin so the router has both halves
	// to union; routing should pick the plugin for "test-echo".
	local := &stubLocal{
		specs:   []provider.ToolSpec{{Name: "read"}},
		outputs: map[string]string{"read": "local-read-output"},
	}
	r := NewToolRouter(local, h)

	// Specs unions both.
	specNames := map[string]bool{}
	for _, s := range r.Specs() {
		specNames[s.Name] = true
	}
	if !specNames["read"] || !specNames["test-echo"] {
		t.Errorf("Specs union missing entries: %v", specNames)
	}

	// Local builtin still runs locally.
	if out, err := r.Run(t.Context(), "read", []byte(`{}`)); err != nil || out != "local-read-output" {
		t.Errorf("local read dispatched wrong: out=%q err=%v", out, err)
	}

	// Plugin tool dispatches via gRPC. Agent/run context propagates so
	// the plugin can scope behavior.
	ctx := WithAgentID(t.Context(), "main")
	ctx = WithRunID(ctx, "run-abc")
	out, err := r.Run(ctx, "test-echo", []byte(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("plugin dispatch: %v", err)
	}
	// testplugin's RunTool echoes args + agent + run.
	if !strings.Contains(out, `{"text":"hello"}`) {
		t.Errorf("output should echo args: %q", out)
	}
	if !strings.Contains(out, "agent=main") {
		t.Errorf("output should include agent id: %q", out)
	}
	if !strings.Contains(out, "run=run-abc") {
		t.Errorf("output should include run id: %q", out)
	}
}

