package toolgate

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/guygrigsby/jess/tool"
	"github.com/guygrigsby/pinion/effect"
	"github.com/guygrigsby/pinion/policy"
)

// RunGate is the per-run (per-turn) state the tool wrappers share: the Level 1
// per-call gate, the Level 2 flow accumulator, the workspace used for scope
// resolution, and a mutex serializing classification (a turn may dispatch
// several tool calls concurrently). A fresh RunGate is built each turn, so the
// flow accumulator resets between turns.
type RunGate struct {
	mu        sync.Mutex
	gate      *Gate
	acc       *Accumulator
	workspace string
}

// NewRunGate builds a per-turn gate from a grant + assessor (nil assessor
// defaults to policy.Default()) and the workspace used for scope resolution.
func NewRunGate(grant effect.Grant, assessor policy.Assessor, workspace string) *RunGate {
	return &RunGate{
		gate:      NewGate(grant, assessor),
		acc:       NewAccumulatorWith(assessor),
		workspace: workspace,
	}
}

// classify runs Level 1 and Level 2 for one call under the lock and returns the
// merged (worst) decision.
func (rg *RunGate) classify(name string, args json.RawMessage) Decision {
	effects := EffectsFor(name, args, rg.workspace)
	rg.mu.Lock()
	defer rg.mu.Unlock()
	d1 := rg.gate.Classify1(effects)
	d2 := rg.acc.Add(name, effects)
	return mergeDecisions(d1, d2)
}

// mergeDecisions combines the per-call and flow decisions: the higher (more
// restrictive) verdict wins and the findings are unioned. The grant delta comes
// from the per-call decision (the flow gate has no grant notion).
func mergeDecisions(perCall, flow Decision) Decision {
	out := Decision{Delta: perCall.Delta}
	out.Verdict = max(perCall.Verdict, flow.Verdict)
	out.Findings = append(append([]policy.Finding(nil), perCall.Findings...), flow.Findings...)
	return out
}

// gatedTool wraps a jess tool.Tool so each Execute is classified and gated
// before the inner tool runs. On a non-Allow verdict it returns a model-visible
// refusal (normal JSON, not a transport error) and does not call the inner
// tool, so the agent can read the refusal and adapt.
type gatedTool struct {
	inner tool.Tool
	rg    *RunGate
}

// Wrap returns inner gated by rg. Trusted first-party control-plane tools
// should be skipped by the caller (see TrustedInternalTool); Wrap itself gates
// unconditionally.
func Wrap(inner tool.Tool, rg *RunGate) tool.Tool {
	return gatedTool{inner: inner, rg: rg}
}

func (g gatedTool) Name() string           { return g.inner.Name() }
func (g gatedTool) Description() string    { return g.inner.Description() }
func (g gatedTool) Schema() map[string]any { return g.inner.Schema() }

func (g gatedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	d := g.rg.classify(g.inner.Name(), args)
	if d.Verdict != policy.Allow {
		return refusalJSON(d), nil
	}
	return g.inner.Execute(ctx, args)
}

// TrustedInternalTool reports whether name is a talon/jess first-party
// control-plane tool that is exempt from effect gating. These tools take
// structured, bounded inputs and operate on fixed talon-owned stores (the
// memory store, the onboarding state, the Claude-notes index) — the model
// cannot point them at an arbitrary path, command, or URL — so they carry none
// of the general-purpose fs/exec/net capability the gate exists to govern.
//
// NOTE (ADR 0017 deviation): the ADR table maps claude_memory to fs.read.
// It is exempted here because its slug-keyed access to a fixed notes index is
// not arbitrary filesystem reach; gating it as generic fs.read would deny it
// under the default workspace grant and break ADR 0013 with no safety gain.
func TrustedInternalTool(name string) bool {
	switch name {
	case "remember", "recall", "finish_onboarding", "claude_memory":
		return true
	default:
		return false
	}
}

// DefaultGrant is the authored default per-agent grant from ADR 0017: fs.read +
// fs.write scoped to the agent workspace, with no exec, net, or secrets access.
// Out of the box, reads/writes inside the workspace pass; bash (exec) and any
// network or secret access are denied unless the grant is widened.
func DefaultGrant(workspace string) effect.Grant {
	g := workspaceGlob(workspace)
	return effect.Grant{Allowed: []effect.Effect{
		{Kind: effect.FSRead, Scope: effect.Scope{Pattern: g}},
		{Kind: effect.FSWrite, Scope: effect.Scope{Pattern: g}},
	}}
}

// refusalJSON renders a model-visible refusal: a normal tool result the agent
// sees and can react to, carrying the verdict and the reasons behind it.
func refusalJSON(d Decision) json.RawMessage {
	reasons := make([]string, 0, len(d.Findings)+len(d.Delta))
	for _, f := range d.Findings {
		reasons = append(reasons, f.RuleID+": "+f.Reason)
	}
	for _, e := range d.Delta {
		scope := e.Scope.Pattern
		if scope == "" {
			scope = "(unscoped)"
		}
		reasons = append(reasons, "ungranted capability "+string(e.Kind)+" "+scope)
	}
	b, _ := json.Marshal(map[string]any{
		"refused": true,
		"verdict": VerdictString(d.Verdict),
		"reasons": reasons,
	})
	return b
}

// VerdictString renders a pinion verdict for model-visible results and audit.
func VerdictString(v policy.Verdict) string {
	switch v {
	case policy.Deny:
		return "deny"
	case policy.NeedsApproval:
		return "needs-approval"
	default:
		return "allow"
	}
}
