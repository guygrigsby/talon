package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/guygrigsby/talon/internal/config"
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

// Register wires agents.list, models.list, and config.schema into r.
func (h *ReadHandler) Register(r *Registry) {
	r.Register("agents.list", h.handleAgentsList)
	r.Register("models.list", h.handleModelsList)
	r.Register("config.schema", h.handleConfigSchema)
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
