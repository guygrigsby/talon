// Package chatdriver is the in-progress replacement for talon's
// inline chat loop in internal/server/chat.go. It builds
// `agentcore.Agent` instances from talon's merged config, resolves
// secrets for agentcore/llm providers, and adapts `agentcore.Event`
// streams onto talon's chat.event wire frames.
//
// Status: Phase 3 of `docs/migration-agentcore.md`. The gateway
// wires this handler for every chat provider; provider-specific
// compatibility fixes live in provider shims below agentcore.
//
// This file: model picking. Mirrors the tier order used by
// internal/server/reads.go's agents.list and the existing chat
// handler's resolution: per-session override (handled outside this
// package), per-agent .model.primary, per-agent .model (string),
// then agents.defaults.model.primary.
package chatdriver

import (
	"github.com/tidwall/gjson"
)

// ModelChoice carries everything BuildAgent needs from the model
// resolution step: the fully-qualified id ("openai/gpt-5.4-mini")
// split into provider + model segments, plus the fallback chain so
// retry logic later can reach for the next entry.
type ModelChoice struct {
	// ID is the fully-qualified "<provider>/<model>" form. Empty
	// means no model is configured for this agent.
	ID string
	// Provider is the segment before the slash. "" when ID is "".
	Provider string
	// Model is the segment after the slash. Equal to ID when there
	// is no slash (defensive; shouldn't happen with sane config).
	Model string
	// Fallbacks are alternate fully-qualified ids the caller may
	// retry against when the primary errors. Order preserved from
	// config.
	Fallbacks []string
}

// ResolveModel returns the model the named agent should use.
// Resolution order:
//
//  1. per-agent `agents.list[<i>].model.primary` (where i is the
//     agent whose .id matches agentID)
//  2. per-agent `agents.list[<i>].model` (runtime shape: a bare
//     string in place of the model object)
//  3. `agents.defaults.model.primary`
//
// Returns a ModelChoice with empty ID when no source produced a
// model — callers decide whether that's an error (chat) or fine
// (e.g. an agent that exists only for plugin dispatch).
func ResolveModel(merged []byte, agentID string) ModelChoice {
	if agentID == "" {
		agentID = "main"
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

	primary := defaultPrimary
	fallbacks := defaultFallbacks

	// Walk agents.list for a matching id; the array form mirrors
	// the existing config schema (id-keyed merge target).
	var agentNode gjson.Result
	gjson.GetBytes(merged, "agents.list").ForEach(func(_, a gjson.Result) bool {
		if a.Get("id").Str == agentID {
			agentNode = a
			return false
		}
		return true
	})
	if agentNode.Exists() {
		if v := agentNode.Get("model.primary"); v.Exists() && v.Str != "" {
			primary = v.Str
		} else if v := agentNode.Get("model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
			primary = v.Str
		}
		// Per-agent fallback override only when the array is
		// non-empty; an empty array on the agent shouldn't wipe
		// the defaults silently.
		var perAgentFallbacks []string
		agentNode.Get("model.fallbacks").ForEach(func(_, v gjson.Result) bool {
			if v.Type == gjson.String && v.Str != "" {
				perAgentFallbacks = append(perAgentFallbacks, v.Str)
			}
			return true
		})
		if len(perAgentFallbacks) > 0 {
			fallbacks = perAgentFallbacks
		}
	}

	out := ModelChoice{ID: primary, Fallbacks: fallbacks}
	if primary != "" {
		for i := 0; i < len(primary); i++ {
			if primary[i] == '/' {
				out.Provider = primary[:i]
				out.Model = primary[i+1:]
				break
			}
		}
		if out.Provider == "" {
			// No slash. Treat the whole thing as the model with an
			// empty provider — caller must reject this, but we
			// keep the ID intact so the error message can say
			// "model %q has no provider segment" instead of
			// truncating it.
			out.Model = primary
		}
	}
	return out
}

// ModelChoiceFromID parses a fully-qualified model id into a
// ModelChoice. Used for per-session overrides, which have already
// been resolved by the gateway UI/session layer and should replace
// the agent/default model as-is.
func ModelChoiceFromID(id string, fallbacks []string) ModelChoice {
	out := ModelChoice{ID: id, Fallbacks: fallbacks}
	if id != "" {
		for i := 0; i < len(id); i++ {
			if id[i] == '/' {
				out.Provider = id[:i]
				out.Model = id[i+1:]
				break
			}
		}
		if out.Provider == "" {
			out.Model = id
		}
	}
	return out
}
