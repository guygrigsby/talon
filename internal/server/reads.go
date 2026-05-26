package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/memory"
	"github.com/guygrigsby/talon/internal/openclaw"
	plugin "github.com/guygrigsby/talon/internal/plugin/legacy"
	"github.com/tidwall/gjson"
)

// ReadHandler serves the read-only RPCs sourced from talon's merged config:
// agents.list, models.list, config.schema. None of them require provider
// credentials or per-session state, so they live in their own type rather
// than being bolted onto ChatHandler.
type ReadHandler struct {
	paths openclaw.Paths
	// host, when non-nil, lets models.list surface models advertised
	// by loaded plugins via their Manifest.OffersProviders. nil host
	// = catalog + user-config only.
	host *plugin.Host
}

// NewReadHandler constructs a ReadHandler bound to the given Paths. The
// merged config is re-read on each call (cheap; bytes are pulled from the
// already-cached overlay JSON).
func NewReadHandler(paths openclaw.Paths) *ReadHandler {
	return &ReadHandler{paths: paths}
}

// WithHost wires the plugin host so models.list can enumerate
// plugin-advertised providers (the bridge for plugin-dispatched
// model lists like the deepseek Go plugin's deepseekModels).
// Chainable; returns h.
func (h *ReadHandler) WithHost(host *plugin.Host) *ReadHandler {
	h.host = host
	return h
}

// Register wires agents.list, models.list, config.{get,schema},
// agent.identity.get, skills.status, and memory.append into r.
func (h *ReadHandler) Register(r *Registry) {
	r.Register("agents.list", h.handleAgentsList)
	r.Register("agents.files.list", h.handleAgentsFilesList)
	r.Register("agents.files.get", h.handleAgentsFilesGet)
	r.Register("agents.files.set", h.handleAgentsFilesSet)
	r.Register("models.list", h.handleModelsList)
	r.Register("config.get", h.handleConfigGet)
	r.Register("config.set", h.handleConfigSet)
	r.Register("config.schema", h.handleConfigSchema)
	r.Register("agent.identity.get", h.handleAgentIdentityGet)
	r.Register("skills.status", h.handleSkillsStatus)
	r.Register("memory.append", h.handleMemoryAppend)
}

// --- agents.list -----------------------------------------------------------

func (h *ReadHandler) handleAgentsList(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agents.list: " + err.Error()}
	}

	defaults := gjson.GetBytes(merged, "agents.defaults.model")
	defaultPrimary := defaults.Get("primary").Str
	var defaultFallbacks []string
	defaults.Get("fallbacks").ForEach(func(_, v gjson.Result) bool {
		if v.Type == gjson.String && v.Str != "" {
			defaultFallbacks = append(defaultFallbacks, v.Str)
		}
		return true
	})

	var agents []map[string]any
	gjson.GetBytes(merged, "agents.list").ForEach(func(_, agent gjson.Result) bool {
		id := agent.Get("id").Str
		if id == "" {
			return true
		}
		// Always emit a non-empty name. Some UIs hide rows whose
		// label is missing — falling back to the id keeps a freshly
		// configured agent visible even before the user fills in
		// `agents.list[].name`.
		name := agent.Get("name").Str
		if name == "" {
			name = id
		}
		row := map[string]any{"id": id, "name": name}
		if ws := agent.Get("workspace").Str; ws != "" {
			row["workspace"] = ws
		}
		// Model resolution mirrors configAgentResolver tiers:
		// per-agent .model.primary → per-agent .model (string) →
		// agents.defaults.model.primary.
		primary := defaultPrimary
		if v := agent.Get("model.primary"); v.Exists() && v.Str != "" {
			primary = v.Str
		} else if v := agent.Get("model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
			primary = v.Str
		}
		modelEntry := map[string]any{
			"primary":   primary,
			"fallbacks": defaultFallbacks,
		}
		// Attach the human-readable name for the resolved primary
		// model so the UI's "default selection" can render a label
		// instead of the raw id. Resolution: user-defined name from
		// models.providers.<X>.models[id==Y].name → catalog name →
		// (omitted, UI falls back to id).
		if name := resolveModelDisplayName(merged, primary); name != "" {
			modelEntry["primaryName"] = name
		}
		row["model"] = modelEntry
		agents = append(agents, row)
		return true
	})

	defaultID := "main"
	if !hasAgentID(agents, defaultID) && len(agents) > 0 {
		defaultID = agents[0]["id"].(string)
	}

	return map[string]any{
		"agents":    agents,
		"defaultId": defaultID,
		"mainKey":   "main",
		"scope":     "per-sender",
	}, nil
}

// resolveModelDisplayName returns a human-readable label for fullID
// ("openai/gpt-4o"), preferring a user-supplied name from
// models.providers.<provider>.models[id==X].name and falling back to
// the built-in catalog. Returns "" when neither has it; the caller
// omits the field so the UI falls back to displaying the id.
func resolveModelDisplayName(merged []byte, fullID string) string {
	if fullID == "" {
		return ""
	}
	prov, id, ok := strings.Cut(fullID, "/")
	if !ok || prov == "" || id == "" {
		return ""
	}
	// User config wins. There is no in-tree catalog fallback —
	// names that aren't in user config come from plugin discovery
	// at models.list time (ModelDescriptor.Name), which is a
	// different code path. Display callers (agents.list) get the
	// id as a final fallback.
	q := fmt.Sprintf("models.providers.%s.models.#(id==%q).name", prov, id)
	if v := gjson.GetBytes(merged, q); v.Exists() && v.Str != "" {
		return v.Str
	}
	return ""
}

func hasAgentID(agents []map[string]any, id string) bool {
	for _, a := range agents {
		if a["id"] == id {
			return true
		}
	}
	return false
}

// --- models.list -----------------------------------------------------------

func (h *ReadHandler) handleModelsList(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	// Optional {refresh: true} bypasses each plugin's ListProviderModels
	// cache so a user-driven refresh sees newly-pulled / newly-released
	// models without waiting for the TTL.
	var p struct {
		Refresh bool `json:"refresh"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}

	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "models.list: " + err.Error()}
	}

	// Index aliases keyed by "<provider>/<id>" from
	// agents.defaults.models.<key>.alias.
	aliasByKey := map[string]string{}
	gjson.GetBytes(merged, "agents.defaults.models").ForEach(func(k, v gjson.Result) bool {
		if a := v.Get("alias"); a.Exists() && a.Str != "" {
			aliasByKey[k.Str] = a.Str
		}
		return true
	})

	// rowsByKey lets config-defined entries (later) replace catalog
	// defaults (first) on the same "<provider>/<id>" pair so users
	// can override a single field — contextWindow, name — without
	// re-typing the rest of the row.
	rowsByKey := map[string]map[string]any{}
	keyOrder := []string{}

	addRow := func(key string, row map[string]any) {
		if _, exists := rowsByKey[key]; !exists {
			keyOrder = append(keyOrder, key)
		}
		rowsByKey[key] = row
	}

	// User config overlays — first source. There is no in-tree
	// catalog floor: models come from (1) user config, (2) plugin
	// ListProviderModels RPC, (3) LM Studio dynamic discovery. A
	// fresh install with no plugins loaded and no user config
	// returns zero rows by design.
	gjson.GetBytes(merged, "models.providers").ForEach(func(provName, prov gjson.Result) bool {
		providerName := provName.Str
		prov.Get("models").ForEach(func(_, m gjson.Result) bool {
			id := m.Get("id").Str
			if id == "" {
				return true
			}
			key := providerName + "/" + id
			row := map[string]any{
				"id":            id,
				"provider":      providerName,
				"contextWindow": m.Get("contextWindow").Int(),
				"reasoning":     m.Get("reasoning").Bool(),
			}
			if name := m.Get("name").Str; name != "" {
				row["name"] = name
			}
			if api := m.Get("api").Str; api != "" {
				row["api"] = api
			} else if api := prov.Get("api").Str; api != "" {
				row["api"] = api
			}
			if maxTokens := m.Get("maxTokens").Int(); maxTokens > 0 {
				row["maxTokens"] = maxTokens
			}
			var inputs []string
			m.Get("input").ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String && item.Str != "" {
					inputs = append(inputs, item.Str)
				}
				return true
			})
			if inputs != nil {
				row["input"] = inputs
			}
			if cost := modelCostForListRow(merged, key, m); cost != nil {
				row["cost"] = cost
			}
			addRow(key, row)
			return true
		})
		return true
	})

	// Plugin-advertised providers. Each loaded plugin's manifest
	// Plugin model discovery (ListProviderModels RPC + manifest
	// static lists) is intentionally NOT consulted here. Discovery
	// produced too much noise — every provider's /v1/models endpoint
	// surfaces dozens of variants the user doesn't actually use,
	// drowning out the ones they care about. The picker now shows
	// only what the user explicitly listed in
	// `models.providers.<name>.models[]` (plus LM Studio's runtime
	// probe below, which has no static equivalent).
	//
	// Plugins still implement ListProviderModels — it's available
	// for `talon models discover` style commands if we add them
	// later. It's just not auto-merged into the picker.

	// LM Studio discovery: ask the local server what's actually
	// loaded and merge in any rows we don't already have. Failures
	// (LM Studio not running, timeout, etc.) degrade silently — the
	// catalog + user config still surface. Discovery rows lose to
	// any same-key row already present so user overrides stick.
	if dm, err := callDiscoverLMStudio(ctx, h.paths, merged); err == nil {
		for _, row := range dm {
			id, _ := row["id"].(string)
			if id == "" {
				continue
			}
			key := "lmstudio/" + id
			if _, exists := rowsByKey[key]; exists {
				continue
			}
			addRow(key, row)
		}
	}

	// Apply alias mappings + flatten in insertion order.
	models := make([]map[string]any, 0, len(keyOrder))
	for _, k := range keyOrder {
		row := rowsByKey[k]
		if alias, ok := aliasByKey[k]; ok {
			row["alias"] = alias
		}
		models = append(models, row)
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i]["id"].(string) < models[j]["id"].(string)
	})

	return map[string]any{"models": models}, nil
}

func modelCostForListRow(merged []byte, modelKey string, model gjson.Result) map[string]any {
	cost := map[string]any{}
	if raw := model.Get("cost"); raw.Exists() {
		copyCostField(cost, raw, "input")
		copyCostField(cost, raw, "output")
		copyCostField(cost, raw, "cacheRead")
		copyCostField(cost, raw, "cacheWrite")
		if len(cost) > 0 {
			cost["source"] = "catalog"
		}
	}

	if override, ok := configuredModelPrice(merged, modelKey); ok {
		cost["input"] = override.In
		cost["output"] = override.Out
		cost["source"] = "priceUsdPer1M"
		return cost
	}

	if len(cost) > 0 {
		return cost
	}
	if price, ok := builtinModelPrices[modelKey]; ok {
		return map[string]any{
			"input":  price.In,
			"output": price.Out,
			"source": "builtin",
		}
	}
	return nil
}

func copyCostField(out map[string]any, raw gjson.Result, field string) {
	if v := raw.Get(field); v.Exists() && v.Type == gjson.Number {
		out[field] = v.Float()
	}
}

// --- agent.identity.get ----------------------------------------------------

type agentIdentityParams struct {
	AgentID    string `json:"agentId"`
	SessionKey string `json:"sessionKey"`
}

// handleAgentIdentityGet resolves the requested agent in three tiers,
// matching openclaw's plugin-bootstrap surface: explicit agentId →
// derived from sessionKey → default "main". The openclaw web UI calls
// this with {sessionKey} and never agentId, so requiring agentId would
// silently break the chat label (the UI's catch swallows the error and
// the assistant stays as "Assistant" forever).
func (h *ReadHandler) handleAgentIdentityGet(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p agentIdentityParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agent.identity.get: " + err.Error()}
		}
	}
	agentID := p.AgentID
	if agentID == "" && p.SessionKey != "" {
		agentID = AgentIDFromSessionKey(p.SessionKey)
	}
	if agentID == "" {
		agentID = "main"
	}
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agent.identity.get: " + err.Error()}
	}

	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if !agent.Exists() {
		// UI tolerates null — let the chat fall back to the agent id.
		return nil, nil
	}
	workspace := agent.Get("workspace").Str
	if workspace == "" {
		workspace = gjson.GetBytes(merged, "agents.defaults.workspace").Str
	}
	if workspace == "" {
		return nil, nil
	}

	identityPath := workspace + "/IDENTITY.md"
	raw, err := os.ReadFile(identityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agent.identity.get: read IDENTITY.md: " + err.Error()}
	}

	id := parseIdentityMarkdown(raw)
	if id.name == "" && id.emoji == "" && id.avatar == "" {
		return nil, nil
	}

	resp := map[string]any{
		"agentId": agentID,
		"name":    id.name,
		"avatar":  pickAvatar(id.avatar, id.emoji),
		"emoji":   id.emoji,
	}
	return resp, nil
}

// pickAvatar mirrors openclaw's behavior: if the avatar field is empty,
// the emoji doubles as the rendered avatar.
func pickAvatar(avatar, emoji string) string {
	if avatar != "" {
		return avatar
	}
	return emoji
}

type identityFields struct {
	name   string
	emoji  string
	avatar string
}

// parseIdentityMarkdown extracts the three IDENTITY.md fields the UI cares
// about. The expected format (per openclaw's bootstrap) is a markdown
// bullet list:
//
//   - **Name:** Clawdia
//   - **Emoji:** 🦞
//   - **Avatar:** avatars/openclaw.png
//
// Lines may use either `- **Key:** value` or `- **Key:**\n  value`. Trailing
// italics or markdown decorations on the value line are stripped. Empty
// values yield empty strings.
func parseIdentityMarkdown(raw []byte) identityFields {
	var out identityFields
	lines := splitLines(string(raw))
	for i := 0; i < len(lines); i++ {
		key, val, ok := matchBulletKV(lines[i])
		if !ok {
			continue
		}
		// If val is empty, peek at the next line in case it carries a
		// continuation. Skip pure-italic continuation hints like
		// "_(workspace-relative path...)_".
		if val == "" && i+1 < len(lines) {
			next := stripContinuation(lines[i+1])
			if next != "" {
				val = next
			}
		}
		switch normalizeKey(key) {
		case "name":
			out.name = val
		case "emoji":
			out.emoji = val
		case "avatar":
			out.avatar = val
		}
	}
	return out
}

// matchBulletKV recognizes "- **Key:** value" and returns (key, value, ok).
// Tolerant of varying whitespace and case in "Key" but strict on the
// leading "- **" marker.
func matchBulletKV(line string) (key, value string, ok bool) {
	const marker = "- **"
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, marker) {
		return "", "", false
	}
	rest := t[len(marker):]
	// Find the closing ":**" of the key.
	end := strings.Index(rest, ":**")
	if end < 0 {
		return "", "", false
	}
	key = rest[:end]
	value = strings.TrimSpace(rest[end+len(":**"):])
	return key, value, true
}

// stripContinuation drops italics-wrapped hint text and quote prefixes so
// "_(workspace-relative path, http(s) URL, or data URI)_" doesn't end up
// being treated as the avatar value.
func stripContinuation(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, ">")
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "_") && strings.HasSuffix(t, "_") {
		return ""
	}
	if strings.HasPrefix(t, "*") && strings.HasSuffix(t, "*") {
		return ""
	}
	return t
}

func normalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func splitLines(s string) []string {
	// Avoid pulling bufio.Scanner just for tests; split on \n and trim
	// trailing \r for Windows-CRLF tolerance.
	out := strings.Split(s, "\n")
	for i, l := range out {
		out[i] = strings.TrimRight(l, "\r")
	}
	return out
}

// --- memory.append ---------------------------------------------------------

type memoryAppendParams struct {
	AgentID    string `json:"agentId"`
	SessionKey string `json:"sessionKey"`
	Text       string `json:"text"`
}

// handleMemoryAppend writes a durable note to the agent's daily memory
// journal. Same on-disk shape as the chat-side `remember` tool — both
// surfaces share the internal/memory writer so a manual RPC append and a
// model-driven remember land in the same file.
//
// Agent resolution: agentId → derived from sessionKey → "main" (matches
// agent.identity.get for consistency).
func (h *ReadHandler) handleMemoryAppend(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p memoryAppendParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "memory.append: " + err.Error()}
		}
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "memory.append: text is required"}
	}
	agentID := p.AgentID
	if agentID == "" && p.SessionKey != "" {
		agentID = AgentIDFromSessionKey(p.SessionKey)
	}
	if agentID == "" {
		agentID = "main"
	}
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "memory.append: " + err.Error()}
	}
	workspace := resolveWorkspace(merged, agentID)
	if workspace == "" {
		return nil, &FrameError{Code: ErrCodeInternal, Message: fmt.Sprintf("memory.append: agent %q has no resolvable workspace", agentID)}
	}
	if err := memory.Append(workspace, p.Text); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "memory.append: " + err.Error()}
	}
	return map[string]any{"ok": true, "agentId": agentID}, nil
}

// --- skills.status ---------------------------------------------------------

type skillsStatusParams struct {
	AgentID string `json:"agentId"`
}

// handleSkillsStatus returns the SkillStatusReport shape the openclaw web
// UI consumes, with an empty skills list. Distinct from the chat tool
// surface — talon's chat tools (read/write/edit/bash/glob/grep) are
// builtin function-calling primitives, not user-installable skills. A
// real skills runtime (scan managedSkillsDir, parse SKILL.md frontmatter)
// is a separate, larger project.
func (h *ReadHandler) handleSkillsStatus(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p skillsStatusParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "skills.status: " + err.Error()}
		}
	}
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "skills.status: " + err.Error()}
	}
	agentID := p.AgentID
	if agentID == "" {
		agentID = "main"
	}
	workspace := resolveWorkspace(merged, agentID)
	managedSkillsDir := ""
	if workspace != "" {
		managedSkillsDir = workspace + "/.skills"
	}
	return map[string]any{
		"workspaceDir":     workspace,
		"managedSkillsDir": managedSkillsDir,
		"skills":           []any{},
	}, nil
}

// resolveWorkspace mirrors configAgentResolver.Workspace's precedence:
// per-agent workspace, fallback to agents.defaults.workspace.
func resolveWorkspace(merged []byte, agentID string) string {
	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if agent.Exists() {
		if v := agent.Get("workspace"); v.Exists() && v.Str != "" {
			return v.Str
		}
	}
	if v := gjson.GetBytes(merged, "agents.defaults.workspace"); v.Exists() && v.Str != "" {
		return v.Str
	}
	return ""
}

// --- config.get ------------------------------------------------------------

// secretLeafKeys is the set of leaf keys whose values get replaced with
// "***REDACTED***" before config.get returns. We don't have openclaw's
// schema-driven uiHints to drive redaction, so this is a conservative
// hardcoded list covering the common openclaw secret-bearing keys
// (gateway.auth.{token,password}, channels.<x>.{token,botToken,apiKey},
// plugins.entries.<x>.config..apiKey, skills.entries.<x>.apiKey, etc).
//
// False negatives are worse than false positives here — match cautiously
// and trust the user to grep before sharing config dumps.
var secretLeafKeys = map[string]bool{
	"token":        true,
	"botToken":     true,
	"password":     true,
	"apiKey":       true,
	"secret":       true,
	"secretKey":    true,
	"clientSecret": true,
	"refreshToken": true,
	"accessToken":  true,
}

const redactedMarker = "***REDACTED***"

// redactSecretsInPlace walks v (typed map[string]any from json.Unmarshal)
// and replaces any string-valued leaf whose key is in secretLeafKeys.
// Non-string values at those keys are left alone — the marker only makes
// sense for strings, and we shouldn't lie about the type.
func redactSecretsInPlace(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretLeafKeys[k] {
				if s, ok := val.(string); ok && s != "" {
					x[k] = redactedMarker
				}
				continue
			}
			redactSecretsInPlace(val)
		}
	case []any:
		for _, item := range x {
			redactSecretsInPlace(item)
		}
	}
}

// handleConfigGet returns the merged config in the openclaw ConfigSnapshot
// envelope shape the UI's controllers/config.ts consumes:
//
//	{path, exists, raw, hash, parsed, valid, config, issues}
//
// raw is the redacted, re-serialized JSON; parsed and config are the same
// redacted map. Hash is sha256 of raw — the UI uses it for optimistic-
// concurrency on writes (config.set, not yet implemented). issues is
// always [] today; future work adds schema-validation results here.
func (h *ReadHandler) handleConfigGet(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "config.get: " + err.Error()}
	}

	var parsed map[string]any
	valid := json.Unmarshal(merged, &parsed) == nil
	if valid {
		redactSecretsInPlace(parsed)
	}

	// Re-serialize the redacted view. Pretty-print so the UI's raw editor
	// is human-readable.
	rawBytes, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "config.get: marshal: " + err.Error()}
	}
	hash := sha256.Sum256(rawBytes)

	_, statErr := os.Stat(h.paths.Talon.Config)
	exists := statErr == nil

	return map[string]any{
		"path":   h.paths.Talon.Config,
		"exists": exists,
		"raw":    string(rawBytes),
		"hash":   hex.EncodeToString(hash[:]),
		"parsed": parsed,
		"valid":  valid,
		"config": parsed,
		"issues": []any{},
	}, nil
}

// --- config.set ------------------------------------------------------------

// handleConfigSet writes a value at a dotted path in the talon
// overlay. Params: {path: "<dotted>", valueJson: "<JSON>", merge: bool?}.
// Empty valueJson deletes the path; otherwise the JSON is parsed
// and config.Set applies it under SetMerge or SetReplaceSafe
// depending on the merge flag.
func (h *ReadHandler) handleConfigSet(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p struct {
		Path      string `json:"path"`
		ValueJSON string `json:"valueJson"`
		Merge     bool   `json:"merge"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "config.set: " + err.Error()}
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "config.set: path is required"}
	}
	segments, err := config.ParsePath(p.Path)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "config.set: parse path: " + err.Error()}
	}
	var value any
	if p.ValueJSON != "" {
		if err := json.Unmarshal([]byte(p.ValueJSON), &value); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "config.set: parse valueJson: " + err.Error()}
		}
	}
	mode := config.SetReplaceSafe
	if p.Merge {
		mode = config.SetMerge
	}
	res, err := config.Set(h.paths, segments, value, config.SetOpts{Mode: mode})
	if err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "config.set: " + err.Error()}
	}
	return map[string]any{
		"path":               res.Path,
		"wrote":              res.Wrote,
		"prunedPaths":        res.PrunedPaths,
		"staleOpenclawPaths": res.StaleOpenclawPaths,
	}, nil
}

// --- config.schema ---------------------------------------------------------

func (h *ReadHandler) handleConfigSchema(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	cachePath := h.paths.Talon.SchemaCachePath()
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache hasn't been populated and we don't yet generate
			// the schema natively (talon-q4m). The web UI calls
			// config.schema on every startup, so erroring here means
			// every page-load shows an error toast. Fall back to a
			// permissive envelope: a JSON Schema that accepts any
			// object. The UI's config editor loses field-level
			// validation hints but the rest of the surface stays
			// usable. Re-running `talon config schema --refresh`
			// against an openclaw gateway populates the real
			// envelope and replaces this stub.
			return permissiveSchemaEnvelope(), nil
		}
		return nil, &FrameError{Code: ErrCodeInternal, Message: "config.schema: read cache: " + err.Error()}
	}
	if !json.Valid(raw) {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "config.schema: cached envelope is not valid JSON; re-run `talon config schema --refresh`"}
	}
	// Pass the envelope through verbatim so the response is byte-identical
	// to upstream gateway output.
	return json.RawMessage(raw), nil
}

// permissiveSchemaEnvelope returns the fallback envelope for the
// no-cache path. Shape mirrors what an openclaw gateway would
// return — generatedAt + schema — so the UI's parsing path stays
// the same. The schema itself is "any object permitted" so the UI's
// editor renders the config tree without per-field constraints.
func permissiveSchemaEnvelope() any {
	return map[string]any{
		"generatedAt": time.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
		"schema": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
	}
}
