package toolgate

import (
	"context"
	"encoding/json"

	"github.com/guygrigsby/pinion"
	"github.com/guygrigsby/pinion/analyze"
	"github.com/guygrigsby/pinion/compose"
	"github.com/guygrigsby/pinion/effect"
	"github.com/guygrigsby/pinion/policy"
)

// Decision is the gate's read on one tool call: the effective verdict, the
// policy findings behind it, and the grant delta (effects the grant does not
// cover).
type Decision struct {
	Verdict  policy.Verdict
	Findings []policy.Finding
	Delta    []effect.Effect
}

// Gate holds the per-agent capability grant and the risk assessor. It is the
// effect-level sibling of talon's name-level toolaccess policy: toolaccess says
// whether a tool may be called at all; the Gate says whether this particular
// call's capabilities are within the agent's grant and free of dangerous flows.
type Gate struct {
	grant    effect.Grant
	assessor policy.Assessor
}

// NewGate constructs a Gate. A nil assessor defaults to policy.Default() (the
// built-in exfiltration/RCE/egress ruleset).
func NewGate(grant effect.Grant, assessor policy.Assessor) *Gate {
	if assessor == nil {
		assessor = policy.Default()
	}
	return &Gate{grant: grant, assessor: assessor}
}

// Classify1 is the Level 1 per-call gate. It assesses the single call's effects
// against both the policy and the grant and returns an effective verdict:
// policy Deny dominates; otherwise a non-empty grant delta escalates an
// otherwise-Allow call to NeedsApproval (which the enforcing wrapper treats as
// deny in v1). Catches "write outside workspace", "exec/net when not granted",
// and any single-call dangerous flow (e.g. an unlabeled MaxDanger tool).
func (g *Gate) Classify1(effects []effect.Effect) Decision {
	fp := footprintOf(effects)
	as := g.assessor.Assess(fp)
	delta := effect.Subset(effects, g.grant)
	v := as.Verdict
	if v == policy.Allow && len(delta) > 0 {
		v = policy.NeedsApproval
	}
	return Decision{Verdict: v, Findings: as.Findings, Delta: delta}
}

// footprintOf builds the footprint of a single opaque call carrying effects, so
// a self-colocated source+sink (e.g. MaxDanger's secrets.read + net.out) lights
// up the exfiltration flow even for a lone call.
func footprintOf(effects []effect.Effect) analyze.Footprint {
	c, err := compose.New().Add("call", callPrim{effects: effects}).Build()
	if err != nil {
		// A single node with no edges cannot fail to build; fall back to a bare
		// footprint so classification stays total.
		return analyze.Footprint{Effects: effects}
	}
	return analyze.Of(c)
}

// callPrim is an opaque pinion.Primitive carrying just an effect set, used to
// drive analyze/policy without an executable body. Its schema declares a single
// "in" input property so the flow accumulator can wire forward edges between
// successive calls (compose.Connect validates the target field exists).
type callPrim struct{ effects []effect.Effect }

func (callPrim) Name() string        { return "call" }
func (callPrim) Description() string { return "" }
func (callPrim) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"in": map[string]any{"type": "string"}}}
}
func (p callPrim) Effects() []effect.Effect {
	return p.effects
}
func (callPrim) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

var _ pinion.Primitive = callPrim{}
