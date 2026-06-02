package chatdriver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/talonpath"
)

func findTool(ts []tool.Tool, name string) tool.Tool {
	for _, t := range ts {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// With the authored default grant (fs read/write inside the workspace, no
// exec), the bash tool on a built agent is gated: invoking it returns a
// model-visible refusal instead of running the command.
func TestBuildAgent_GatesBashByDefault(t *testing.T) {
	clearProviderEnv(t)
	ws := t.TempDir()
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "` + ws + `"},
			"list": [{"id": "main"}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	spec, err := b.prepareBuild("main")
	if err != nil {
		t.Fatalf("prepareBuild: %v", err)
	}
	bash := findTool(spec.toolSet, "bash")
	if bash == nil {
		t.Fatal("bash tool missing from toolset")
	}
	out, err := bash.Execute(context.Background(), []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("execute should not error on refusal: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, out)
	}
	if r["refused"] != true {
		t.Fatalf("bash should be refused by the default grant, got %s", out)
	}
}

// A read inside the workspace passes the gate and runs the inner tool (which
// errors at the fs level for a missing file, but the result is not a gate
// refusal — proving the gate allowed it through).
func TestBuildAgent_AllowsWorkspaceRead(t *testing.T) {
	clearProviderEnv(t)
	ws := t.TempDir()
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "` + ws + `"},
			"list": [{"id": "main"}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	spec, err := b.prepareBuild("main")
	if err != nil {
		t.Fatalf("prepareBuild: %v", err)
	}
	read := findTool(spec.toolSet, "read")
	if read == nil {
		t.Fatal("read tool missing from toolset")
	}
	out, _ := read.Execute(context.Background(), []byte(`{"file_path":"nope.txt"}`))
	var r map[string]any
	_ = json.Unmarshal(out, &r)
	if r["refused"] == true {
		t.Fatalf("read inside workspace should not be gate-refused, got %s", out)
	}
}

// A per-agent toolgate.allow list widens the grant so bash (exec) runs.
func TestBuildAgent_PerAgentExecGrant(t *testing.T) {
	clearProviderEnv(t)
	ws := t.TempDir()
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "` + ws + `"},
			"list": [{"id": "main", "toolgate": {"allow": ["exec"]}}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	spec, err := b.prepareBuild("main")
	if err != nil {
		t.Fatalf("prepareBuild: %v", err)
	}
	bash := findTool(spec.toolSet, "bash")
	out, err := bash.Execute(context.Background(), []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var r map[string]any
	_ = json.Unmarshal(out, &r)
	if r["refused"] == true {
		t.Fatalf("bash should run with exec granted, got %s", out)
	}
}

// toolgate.mode=off bypasses gating entirely: bash runs even without an exec
// grant.
func TestBuildAgent_ModeOffBypassesGate(t *testing.T) {
	clearProviderEnv(t)
	ws := t.TempDir()
	cfg := []byte(`{
		"toolgate": {"mode": "off"},
		"agents": {
			"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}, "workspace": "` + ws + `"},
			"list": [{"id": "main"}]
		}
	}`)
	b := NewBuilder(cfg, talonpath.Paths{}).WithAuthOverride(map[string]ProviderAuth{
		"openai": {Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	})
	spec, err := b.prepareBuild("main")
	if err != nil {
		t.Fatalf("prepareBuild: %v", err)
	}
	bash := findTool(spec.toolSet, "bash")
	out, _ := bash.Execute(context.Background(), []byte(`{"command":"echo hi"}`))
	var r map[string]any
	_ = json.Unmarshal(out, &r)
	if r["refused"] == true {
		t.Fatalf("mode=off should not gate bash, got %s", out)
	}
}
