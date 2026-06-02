package toolgate

import (
	"testing"

	"github.com/guygrigsby/pinion/effect"
	"github.com/guygrigsby/pinion/policy"
)

// The headline guarantee: a read-of-secrets followed by a network-out in the
// same turn completes an exfiltration flow, and the gate denies the sink call.
func TestFlowExfilDeniesNetOutAfterSecretsRead(t *testing.T) {
	acc := NewAccumulator()

	// Call 1: a tool that reads secrets. On its own (no sink yet) it is allowed.
	d1 := acc.Add("read_secrets", []effect.Effect{{Kind: effect.SecretsRead}})
	if d1.Verdict == policy.Deny {
		t.Fatalf("lone secrets.read should not be denied by flow: %+v", d1)
	}

	// Call 2: a tool that writes to the network. The accumulated composition is
	// now secrets.read -> net.out, which is exfiltration: deny.
	d2 := acc.Add("http_post", []effect.Effect{{Kind: effect.NetOut}})
	if d2.Verdict != policy.Deny {
		t.Fatalf("net.out after secrets.read must Deny (exfiltration): %+v", d2)
	}
	if !hasFinding(d2.Findings, "exfiltration") {
		t.Fatalf("expected exfiltration finding, got %+v", d2.Findings)
	}
}

// A benign read-then-write sequence (no source->net sink) stays allowed.
func TestFlowBenignReadThenWriteAllows(t *testing.T) {
	acc := NewAccumulator()
	if d := acc.Add("read", []effect.Effect{{Kind: effect.FSRead, Scope: effect.Scope{Pattern: "/work/a"}}}); d.Verdict == policy.Deny {
		t.Fatalf("read should not be denied: %+v", d)
	}
	// fs.read -> fs.write is not in the default ruleset (only net/exec sinks
	// from sources are), so it stays allowed.
	if d := acc.Add("write", []effect.Effect{{Kind: effect.FSWrite, Scope: effect.Scope{Pattern: "/work/b"}}}); d.Verdict == policy.Deny {
		t.Fatalf("benign read-then-write should stay allowed: %+v", d)
	}
}

// fs.read followed by net.out is data-egress (High -> NeedsApproval, not Allow).
func TestFlowFileReadThenNetOutEscalates(t *testing.T) {
	acc := NewAccumulator()
	acc.Add("read", []effect.Effect{{Kind: effect.FSRead, Scope: effect.Scope{Pattern: "/work/a"}}})
	d := acc.Add("http_post", []effect.Effect{{Kind: effect.NetOut}})
	if d.Verdict == policy.Allow {
		t.Fatalf("file-read then net-out should not Allow (data-egress): %+v", d)
	}
	if !hasFinding(d.Findings, "data-egress") {
		t.Fatalf("expected data-egress finding, got %+v", d.Findings)
	}
}

// Each accumulator is independent: a secrets.read in one turn must not taint a
// net.out in a separate turn (separate accumulator, keyed by run elsewhere).
func TestFlowAccumulatorsAreIsolated(t *testing.T) {
	a := NewAccumulator()
	a.Add("read_secrets", []effect.Effect{{Kind: effect.SecretsRead}})

	b := NewAccumulator()
	d := b.Add("http_post", []effect.Effect{{Kind: effect.NetOut}})
	if d.Verdict == policy.Deny {
		t.Fatalf("net.out in a fresh accumulator must not be denied: %+v", d)
	}
}

func hasFinding(fs []policy.Finding, ruleID string) bool {
	for _, f := range fs {
		if f.RuleID == ruleID {
			return true
		}
	}
	return false
}
