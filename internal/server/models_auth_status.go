package server

// models.authStatus — reports per-provider credential health.
//
// Scope: talon today only supports api_key auth profiles (openai,
// deepseek, lmstudio, plus any plugin providers that read keys from
// disk). OAuth/bearer-token providers with refresh state and time-
// bounded credentials are not implemented yet, so this handler does not emit `expiry`
// objects, and every profile we surface has type="api_key".
//
// The UI's isMonitoredAuthProvider filter only flags providers as
// "attention" if they have an oauth/token profile OR their status
// is "missing", so reporting "missing" for unconfigured providers
// gives the dashboard the signal it needs. "ok" providers stay
// quiet — their api keys don't expire on a schedule the dashboard
// can monitor anyway.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// ModelsAuthStatusHandler serves models.authStatus from the merged
// config (provider enumeration) and per-agent auth-profiles.json
// (credential presence). No external network calls — everything is
// derived from on-disk state.
type ModelsAuthStatusHandler struct {
	paths talonpath.Paths
}

func NewModelsAuthStatusHandler(paths talonpath.Paths) *ModelsAuthStatusHandler {
	return &ModelsAuthStatusHandler{paths: paths}
}

func (h *ModelsAuthStatusHandler) Register(r *Registry) {
	r.Register("models.authStatus", h.handleAuthStatus)
}

// authStatusParams is the optional input. agentId picks which
// agent's auth-profiles.json to inspect; provider narrows the
// response. refresh is accepted but is a no-op because we do not cache today.
type authStatusParams struct {
	AgentID  string `json:"agentId,omitempty"`
	Provider string `json:"provider,omitempty"`
	Refresh  bool   `json:"refresh,omitempty"`
}

// authProfilesFile mirrors the on-disk shape Talon's providers read.
// Only the fields we use here are decoded; extras are ignored for
// forward-compat.
type authProfilesFile struct {
	Profiles map[string]authProfilesRow `json:"profiles"`
}

type authProfilesRow struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"`
}

func (h *ModelsAuthStatusHandler) handleAuthStatus(_ context.Context, _ HandlerCtx, raw json.RawMessage) (any, *FrameError) {
	var p authStatusParams
	if len(raw) > 0 && string(raw) != "null" {
		// Tolerate malformed params; treat junk the same as {} rather than 400.
		_ = json.Unmarshal(raw, &p)
	}
	agentID := p.AgentID
	if agentID == "" {
		// "main" matches lmstudio_discovery.go and the deepseek
		// plugin — the canonical default agent for shared local
		// resources.
		agentID = "main"
	}

	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "models.authStatus: " + err.Error()}
	}

	profiles, ferr := h.readAuthProfiles(agentID)
	if ferr != nil {
		return nil, ferr
	}

	providers := buildProvidersList(merged, profiles, p.Provider)

	return map[string]any{
		"ts":        time.Now().UnixMilli(),
		"providers": providers,
	}, nil
}

// readAuthProfiles loads the agent's auth-profiles.json from Talon state.
// Missing file is not an error — a fresh install has no profiles and every
// provider falls through to "missing".
func (h *ModelsAuthStatusHandler) readAuthProfiles(agentID string) (map[string]authProfilesRow, *FrameError) {
	candidates := []string{
		filepath.Join(h.paths.Talon.AgentDir(agentID), "agent", "auth-profiles.json"),
	}
	for _, path := range candidates {
		buf, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, &FrameError{Code: ErrCodeInternal, Message: fmt.Sprintf("models.authStatus: read %s: %v", path, err)}
		}
		var f authProfilesFile
		if err := json.Unmarshal(buf, &f); err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: fmt.Sprintf("models.authStatus: parse %s: %v", path, err)}
		}
		return f.Profiles, nil
	}
	return nil, nil
}

// buildProvidersList computes one entry per known provider. A
// provider is "known" if it appears under models.providers.* in
// merged config OR in auth-profiles.json OR has a key under one
// of the plugin-config paths (openai-compat tenants, anthropic).
// The union covers "configured with credentials" and "listed but
// unauthed."
//
// Plugin-config keys are injected as synthetic profile rows so the
// downstream buildProviderEntry sees them with the same shape as
// real on-disk profiles. The injected profileId follows the pattern
// "<provider>:openai-compat" or "<provider>:plugin" so the FE can
// distinguish them from genuine auth-profiles.json entries.
func buildProvidersList(merged []byte, profiles map[string]authProfilesRow, filter string) []any {
	if profiles == nil {
		profiles = map[string]authProfilesRow{}
	}

	known := map[string]struct{}{}
	gjson.GetBytes(merged, "models.providers").ForEach(func(name, _ gjson.Result) bool {
		if name.Str != "" {
			known[name.Str] = struct{}{}
		}
		return true
	})

	// openai-compat tenants — each provider entry's apiKey (literal
	// or op://keychain:// ref) counts as a key for that provider.
	gjson.GetBytes(merged, "plugins.entries.openai-compat.config.providers").ForEach(func(name, prov gjson.Result) bool {
		if name.Str == "" {
			return true
		}
		known[name.Str] = struct{}{}
		if k := strings.TrimSpace(prov.Get("apiKey").Str); k != "" {
			pid := name.Str + ":openai-compat"
			if _, exists := profiles[pid]; !exists {
				profiles[pid] = authProfilesRow{
					Type:     "api_key",
					Provider: name.Str,
					Key:      k,
				}
			}
		}
		return true
	})

	// anthropic plugin's dedicated config.apiKey.
	if k := strings.TrimSpace(gjson.GetBytes(merged, "plugins.entries.anthropic.config.apiKey").Str); k != "" {
		known["anthropic"] = struct{}{}
		pid := "anthropic:plugin"
		if _, exists := profiles[pid]; !exists {
			profiles[pid] = authProfilesRow{
				Type:     "api_key",
				Provider: "anthropic",
				Key:      k,
			}
		}
	}

	for _, row := range profiles {
		if row.Provider != "" {
			known[row.Provider] = struct{}{}
		}
	}

	filterLower := strings.ToLower(strings.TrimSpace(filter))

	sortedNames := make([]string, 0, len(known))
	for name := range known {
		if filterLower != "" && strings.ToLower(name) != filterLower {
			continue
		}
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	out := make([]any, 0, len(sortedNames))
	for _, name := range sortedNames {
		out = append(out, buildProviderEntry(name, profiles))
	}
	return out
}

// buildProviderEntry assembles the wire shape for one provider.
// Status is computed bottom-up: profile.status = "ok" when its key
// is present (literal or op://keychain:// ref), "missing" otherwise.
// provider.status = "ok" if any profile is ok, else "missing".
func buildProviderEntry(name string, allProfiles map[string]authProfilesRow) map[string]any {
	type profileOut struct {
		ProfileID string `json:"profileId"`
		Type      string `json:"type"`
		Status    string `json:"status"`
	}

	matched := make([]profileOut, 0)
	hasOK := false
	matchedIDs := make([]string, 0)
	for id, row := range allProfiles {
		if row.Provider != name {
			continue
		}
		matchedIDs = append(matchedIDs, id)
	}
	sort.Strings(matchedIDs)
	for _, id := range matchedIDs {
		row := allProfiles[id]
		status := "missing"
		if strings.TrimSpace(row.Key) != "" {
			status = "ok"
			hasOK = true
		}
		typ := row.Type
		if typ == "" {
			typ = "api_key"
		}
		matched = append(matched, profileOut{
			ProfileID: id,
			Type:      typ,
			Status:    status,
		})
	}

	providerStatus := "missing"
	if hasOK {
		providerStatus = "ok"
	}

	profilesAny := make([]any, 0, len(matched))
	for _, p := range matched {
		profilesAny = append(profilesAny, map[string]any{
			"profileId": p.ProfileID,
			"type":      p.Type,
			"status":    p.Status,
		})
	}

	return map[string]any{
		"provider":    name,
		"displayName": displayNameForProvider(name),
		"status":      providerStatus,
		"profiles":    profilesAny,
	}
}

// displayNameForProvider returns a human-friendly label. Talon's auth
// surface is api-key-only today, so we title-case unknowns and override a
// few common cases inline.
func displayNameForProvider(name string) string {
	switch name {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "deepseek":
		return "DeepSeek"
	case "lmstudio":
		return "LM Studio"
	case "google", "gemini":
		return "Google"
	case "xai":
		return "xAI"
	case "groq":
		return "Groq"
	case "ollama":
		return "Ollama"
	case "openrouter":
		return "OpenRouter"
	}
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
