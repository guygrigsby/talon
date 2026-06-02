package toolgate

import (
	"context"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"enforce": ModeEnforce,
		"audit":   ModeAudit,
		"off":     ModeOff,
		"":        ModeEnforce, // default
		"bogus":   ModeEnforce, // unknown defaults to enforce (fail-safe)
		"ENFORCE": ModeEnforce,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q)=%v, want %v", in, got, want)
		}
	}
	if ModeEnforce.String() != "enforce" || ModeAudit.String() != "audit" || ModeOff.String() != "off" {
		t.Errorf("Mode.String mismatch")
	}
}

// In audit mode the gate classifies and records but never blocks: a call that
// would be denied under enforce still executes, and the sink sees the deny.
func TestAuditModeRecordsButDoesNotBlock(t *testing.T) {
	ran := false
	var got struct {
		tool, verdict string
		reasons       []string
	}
	rg := NewRunGate(DefaultGrant("/work"), nil, "/work").
		WithMode(ModeAudit).
		WithSink(func(tool, verdict string, reasons []string) {
			got.tool, got.verdict, got.reasons = tool, verdict, reasons
		})
	gt := Wrap(fakeTool{name: "bash", ran: &ran, out: `{"output":"hi"}`}, rg)

	out, err := gt.Execute(context.Background(), []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ran {
		t.Fatal("audit mode must still execute the inner tool")
	}
	if refused, _ := decodeRefusal(t, out); refused {
		t.Fatalf("audit mode must not refuse: %s", out)
	}
	if got.tool != "bash" || got.verdict == "allow" {
		t.Fatalf("sink should record bash as non-allow, got %+v", got)
	}
	if len(got.reasons) == 0 {
		t.Fatalf("sink should carry reasons for a non-allow verdict")
	}
}

// The sink fires on allow too, so audit-only mode produces a record for every
// classification.
func TestSinkFiresOnAllow(t *testing.T) {
	fired := false
	rg := NewRunGate(DefaultGrant("/work"), nil, "/work").
		WithSink(func(tool, verdict string, reasons []string) {
			if tool == "read" && verdict == "allow" {
				fired = true
			}
		})
	gt := Wrap(fakeTool{name: "read", out: `{}`}, rg)
	if _, err := gt.Execute(context.Background(), []byte(`{"file_path":"a.txt"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !fired {
		t.Fatal("sink should fire on an allowed call")
	}
}

// Enforce mode (the default) still blocks.
func TestEnforceModeBlocks(t *testing.T) {
	ran := false
	rg := NewRunGate(DefaultGrant("/work"), nil, "/work") // default mode = enforce
	gt := Wrap(fakeTool{name: "bash", ran: &ran}, rg)
	out, _ := gt.Execute(context.Background(), []byte(`{"command":"x"}`))
	if ran {
		t.Fatal("enforce mode must block bash")
	}
	if refused, _ := decodeRefusal(t, out); !refused {
		t.Fatalf("enforce mode must refuse: %s", out)
	}
}
