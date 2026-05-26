package agentcore_chat

import (
	"reflect"
	"testing"
)

func TestResolveModel_PerAgentPrimaryWins(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/default-1"}},
			"list": [
				{"id": "coding", "model": {"primary": "anthropic/claude-opus-4-7"}}
			]
		}
	}`)
	got := ResolveModel(cfg, "coding")
	if got.ID != "anthropic/claude-opus-4-7" || got.Provider != "anthropic" || got.Model != "claude-opus-4-7" {
		t.Errorf("got = %+v", got)
	}
}

func TestResolveModel_PerAgentLegacyStringShape(t *testing.T) {
	// Older config shape: agents.list[].model is a bare string
	// instead of an object with .primary. Resolver should still
	// pick it up.
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/default-1"}},
			"list": [
				{"id": "research", "model": "anthropic/claude-sonnet-4-6"}
			]
		}
	}`)
	got := ResolveModel(cfg, "research")
	if got.ID != "anthropic/claude-sonnet-4-6" {
		t.Errorf("got ID = %q", got.ID)
	}
}

func TestResolveModel_FallsBackToDefaults(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {
				"model": {
					"primary": "openai/gpt-5.4-mini",
					"fallbacks": ["anthropic/claude-opus-4-7", "deepseek/deepseek-reasoner"]
				}
			},
			"list": [{"id": "main"}]
		}
	}`)
	got := ResolveModel(cfg, "main")
	if got.ID != "openai/gpt-5.4-mini" || got.Provider != "openai" || got.Model != "gpt-5.4-mini" {
		t.Errorf("got = %+v", got)
	}
	if !reflect.DeepEqual(got.Fallbacks, []string{"anthropic/claude-opus-4-7", "deepseek/deepseek-reasoner"}) {
		t.Errorf("fallbacks = %v", got.Fallbacks)
	}
}

func TestResolveModel_EmptyAgentIDDefaultsToMain(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "openai/x"}},
			"list": [{"id": "main", "model": {"primary": "anthropic/y"}}]
		}
	}`)
	if got := ResolveModel(cfg, ""); got.ID != "anthropic/y" {
		t.Errorf("empty agentID should default to main, got %q", got.ID)
	}
}

func TestResolveModel_NoSlashKeepsIDButEmptyProvider(t *testing.T) {
	// Defensive: a model id without a provider segment should
	// surface intact so callers can produce a useful error.
	cfg := []byte(`{
		"agents": {"defaults": {"model": {"primary": "broken-no-slash"}}}
	}`)
	got := ResolveModel(cfg, "main")
	if got.ID != "broken-no-slash" {
		t.Errorf("ID = %q, want preserved", got.ID)
	}
	if got.Provider != "" {
		t.Errorf("Provider should be empty when no slash, got %q", got.Provider)
	}
	if got.Model != "broken-no-slash" {
		t.Errorf("Model = %q, want whole id as fallback", got.Model)
	}
}

func TestResolveModel_AgentFallbacksOverrideDefaults(t *testing.T) {
	cfg := []byte(`{
		"agents": {
			"defaults": {"model": {
				"primary": "openai/default",
				"fallbacks": ["anthropic/x"]
			}},
			"list": [
				{"id": "coding", "model": {"primary": "anthropic/claude-opus-4-7", "fallbacks": ["deepseek/r"]}}
			]
		}
	}`)
	got := ResolveModel(cfg, "coding")
	if !reflect.DeepEqual(got.Fallbacks, []string{"deepseek/r"}) {
		t.Errorf("fallbacks = %v, want per-agent override", got.Fallbacks)
	}
}

func TestResolveModel_EmptyConfigEmptyResult(t *testing.T) {
	got := ResolveModel([]byte(`{}`), "main")
	if got.ID != "" || got.Provider != "" || got.Model != "" {
		t.Errorf("empty config should produce zero ModelChoice, got %+v", got)
	}
}

func TestModelChoiceFromID_PreservesFallbacks(t *testing.T) {
	got := ModelChoiceFromID("openai/gpt-4o-mini", []string{"anthropic/fallback"})
	if got.ID != "openai/gpt-4o-mini" || got.Provider != "openai" || got.Model != "gpt-4o-mini" {
		t.Errorf("got = %+v", got)
	}
	if !reflect.DeepEqual(got.Fallbacks, []string{"anthropic/fallback"}) {
		t.Errorf("fallbacks = %v", got.Fallbacks)
	}
}
