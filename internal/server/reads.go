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

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/memory"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
)

// ReadHandler serves the read-only RPCs sourced from talon's merged config:
// agents.list, models.list, config.schema. None of them require provider
// credentials or per-session state, so they live in their own type rather
// than being bolted onto ChatHandler.
type ReadHandler struct {
	paths openclaw.Paths
}

// NewReadHandler constructs a ReadHandler bound to the given Paths. The
// merged config is re-read on each call (cheap; bytes are pulled from the
// already-cached overlay JSON).
func NewReadHandler(paths openclaw.Paths) *ReadHandler {
	return &ReadHandler{paths: paths}
}

// Register wires agents.list, models.list, config.{get,schema},
// agent.identity.get, skills.status, and memory.append into r.
func (h *ReadHandler) Register(r *Registry) {
	r.Register("agents.list", h.handleAgentsList)
	r.Register("models.list", h.handleModelsList)
	r.Register("config.get", h.handleConfigGet)
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
		row := map[string]any{"id": id}
		if name := agent.Get("name").Str; name != "" {
			row["name"] = name
		}
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
		row["model"] = map[string]any{
			"primary":   primary,
			"fallbacks": defaultFallbacks,
		}
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

func hasAgentID(agents []map[string]any, id string) bool {
	for _, a := range agents {
		if a["id"] == id {
			return true
		}
	}
	return false
}

// --- models.list -----------------------------------------------------------

func (h *ReadHandler) handleModelsList(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
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

	var models []map[string]any
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
			if alias, ok := aliasByKey[key]; ok {
				row["alias"] = alias
			}
			models = append(models, row)
			return true
		})
		return true
	})

	sort.Slice(models, func(i, j int) bool {
		return models[i]["id"].(string) < models[j]["id"].(string)
	})

	return map[string]any{"models": models}, nil
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
//	- **Name:** Clawdia
//	- **Emoji:** 🦞
//	- **Avatar:** avatars/openclaw.png
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
	"token":     true,
	"botToken":  true,
	"password":  true,
	"apiKey":    true,
	"secret":    true,
	"secretKey": true,
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

// --- config.schema ---------------------------------------------------------

func (h *ReadHandler) handleConfigSchema(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	cachePath := h.paths.Talon.SchemaCachePath()
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &FrameError{
				Code:    ErrCodeInternal,
				Message: fmt.Sprintf("config.schema: cache empty at %s; populate it with `talon config schema --refresh` against an upstream openclaw gateway, or wait for native schema generation (talon-q4m)", cachePath),
			}
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
