package toolgate

import (
	"fmt"

	"github.com/guygrigsby/pinion/analyze"
	"github.com/guygrigsby/pinion/compose"
	"github.com/guygrigsby/pinion/effect"
	"github.com/guygrigsby/pinion/policy"
)

// Accumulator is the Level 2 cross-call flow gate for a single turn. It records
// each tool call as a node in a compose composition with forward edges
// (call N-1 -> call N), conservatively modeling "data from earlier calls may
// reach later calls". On each Add it builds the composition-so-far, runs
// analyze.Of -> policy.Assess, and returns the verdict — denying the call that
// completes a dangerous flow (e.g. a prior secrets.read reaching this net.out).
//
// One Accumulator covers one turn (the unit a single prompt's tool loop runs
// in); callers key a fresh Accumulator per run and drop it at turn end. It is
// not safe for concurrent use — a turn's tool calls are sequential.
type Accumulator struct {
	assessor policy.Assessor
	nodes    []flowNode
}

type flowNode struct {
	id      string
	effects []effect.Effect
}

// NewAccumulator returns an empty per-turn flow accumulator using the built-in
// policy ruleset.
func NewAccumulator() *Accumulator {
	return &Accumulator{assessor: policy.Default()}
}

// NewAccumulatorWith returns a flow accumulator using a specific assessor.
func NewAccumulatorWith(a policy.Assessor) *Accumulator {
	if a == nil {
		a = policy.Default()
	}
	return &Accumulator{assessor: a}
}

// Add records a call and returns the verdict for the composition of all calls
// so far. The verdict reflects every flow the new call may complete; a benign
// call leaves earlier allow verdicts intact, while a sink that closes a
// source->sink path trips the corresponding rule.
func (a *Accumulator) Add(name string, effects []effect.Effect) Decision {
	a.nodes = append(a.nodes, flowNode{id: fmt.Sprintf("n%d", len(a.nodes)), effects: effects})

	b := compose.New()
	for _, n := range a.nodes {
		b.Add(compose.NodeID(n.id), callPrim{effects: n.effects})
	}
	for i := 1; i < len(a.nodes); i++ {
		b.Connect(compose.NodeID(a.nodes[i-1].id), compose.NodeID(a.nodes[i].id), "out", "in")
	}
	c, err := b.Build()
	if err != nil {
		// Conservative: if the composition can't be built, deny rather than let
		// an unanalyzable call through.
		return Decision{Verdict: policy.Deny}
	}
	as := a.assessor.Assess(analyze.Of(c))
	return Decision{Verdict: as.Verdict, Findings: as.Findings}
}
