package connectapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
)

// Adversarial tests for the JSON pass-through path (cron.add and the
// jsonUnmarshalAny helper) and the FrameError->Connect code mapping. The
// pass-through is the widest attack surface: arbitrary client JSON reaches a
// registry handler, so it must reject or contain anything thrown at it without
// panicking or hanging.

// jsonUnmarshalAny must never panic and must return an error (not silently
// succeed) on structurally invalid or pathological input.
func TestJSONUnmarshalAny_Hostile(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is no-op", "", false},
		{"whitespace only", "   \n\t", true},
		{"trailing garbage", "123 garbage", true},
		{"two values", `{} {}`, true},
		{"unterminated object", `{"a":`, true},
		{"unterminated string", `"abc`, true},
		{"NaN literal", "NaN", true},
		{"Infinity literal", "Infinity", true},
		{"bare comma", ",", true},
		{"duplicate keys (last wins, no error)", `{"a":1,"a":2}`, false},
		{"lone surrogate becomes U+FFFD", `"\ud800"`, false},
		{"huge exponent overflows float64", "1e400", true},
		{"valid nested", `{"a":{"b":[1,2,3]}}`, false},
		// Deeply nested input must hit encoding/json's depth limit and error
		// out rather than overflow the stack.
		{"deeply nested arrays", strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000), true},
		{"deeply nested objects", strings.Repeat(`{"a":`, 100_000) + "1" + strings.Repeat("}", 100_000), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v any
			err := jsonUnmarshalAny(c.in, &v) // must not panic
			if c.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil (v=%#v)", c.name, v)
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", c.name, err)
			}
		})
	}
}

// FuzzJSONUnmarshalAny: the contract is simply "never panic" for any input.
// Run with: go test -run x -fuzz FuzzJSONUnmarshalAny ./internal/connectapi
func FuzzJSONUnmarshalAny(f *testing.F) {
	for _, s := range []string{
		"", "null", "{}", "[]", `{"a":1}`, "NaN", `"\ud800"`,
		strings.Repeat("[", 5000), `{"a":{"b":{"c":1}}}`, "1e400",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var v any
		_ = jsonUnmarshalAny(s, &v) // only assertion: no panic
	})
}

// CronService.Add parses the raw JSON body before dispatching. Malformed JSON
// must be rejected with InvalidArgument at the parse step — before the handler
// runs — so a bad body can't reach (or crash) the registry. Reg is nil here on
// purpose: the malformed-input path must return before ever touching it.
func TestCronAdd_MalformedJSONRejectedBeforeDispatch(t *testing.T) {
	svc := &CronService{Reg: nil}
	bad := []string{
		"{not json",
		`{"schedule":}`,
		"[1,2,",
		`"unterminated`,
		strings.Repeat("[", 100_000), // unbalanced + deep
	}
	for _, body := range bad {
		t.Run(body[:min(len(body), 12)], func(t *testing.T) {
			_, err := svc.Add(context.Background(), connect.NewRequest(&talonv1.JSONPayload{Json: body}))
			if err == nil {
				t.Fatalf("malformed cron.add body %q was accepted", body)
			}
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
				t.Errorf("got %v, want CodeInvalidArgument", err)
			}
		})
	}
}
