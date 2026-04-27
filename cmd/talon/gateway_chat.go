package main

import (
	"fmt"
	"path/filepath"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/tidwall/gjson"
)

// configAgentResolver implements server.AgentResolver by reading talon's
// merged config on each call. Cheap enough that we don't cache: the merged
// view is parsed in-memory from already-cached overlay bytes.
//
// Model resolution mirrors openclaw's precedence:
//
//  1. per-agent agents.list[id==X].model.primary (object form)
//  2. per-agent agents.list[id==X].model         (string shorthand)
//  3. agents.defaults.model.primary              (global default)
//
// First non-empty hit wins. If the agent isn't in agents.list at all,
// returns ErrAgentNotFound.
type configAgentResolver struct {
	paths openclaw.Paths
}

func (r *configAgentResolver) PrimaryModel(agentID string) (provider.ModelID, error) {
	merged, err := config.MergedBytes(r.paths)
	if err != nil {
		return "", fmt.Errorf("read merged config: %w", err)
	}
	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if !agent.Exists() {
		return "", fmt.Errorf("%w: %q", server.ErrAgentNotFound, agentID)
	}
	// Per-agent model: object form first, then string shorthand.
	if v := agent.Get("model.primary"); v.Exists() && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	if v := agent.Get("model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	// Global default.
	if v := gjson.GetBytes(merged, "agents.defaults.model.primary"); v.Exists() && v.Str != "" {
		return provider.ModelID(v.Str), nil
	}
	return "", fmt.Errorf("agent %q has no resolvable model (no per-agent model and no agents.defaults.model.primary)", agentID)
}

// agentProviderFactory implements server.ProviderFactory. For each request
// it looks up the per-agent auth-profiles.json under the openclaw layer
// (talon does not write credentials to its overlay). Currently only
// "openai" has a concrete implementation; everything else returns
// ErrProviderUnavailable so the caller surfaces a useful error.
type agentProviderFactory struct {
	paths openclaw.Paths
}

func (f *agentProviderFactory) For(providerName, agentID string) (provider.Provider, error) {
	switch providerName {
	case "openai":
		authPath := filepath.Join(f.paths.Openclaw.AgentDir(agentID), "agent", "auth-profiles.json")
		key, err := openai.LoadAPIKey(authPath, "openai:default")
		if err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
		return openai.New(openai.Options{APIKey: key}), nil
	default:
		return nil, fmt.Errorf("%w: %q (only 'openai' is implemented)", server.ErrProviderUnavailable, providerName)
	}
}
