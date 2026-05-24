package connectapi

import (
	"context"
	"errors"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/guygrigsby/talon/internal/server"
)

// Bridge-level tests. The bridge is purely-Go logic — no server,
// no HTTP — so we can stand up a stub Registry and verify the
// translation rules without booting a gateway. Live end-to-end
// (Connect → Registry → real handlers) is covered by the smoke
// script in the commit body.

func TestFrameErrorToConnect_CodeMapping(t *testing.T) {
	cases := []struct {
		legacyCode string
		want       connect.Code
	}{
		{server.ErrCodeBadRequest, connect.CodeInvalidArgument},
		{server.ErrCodeUnauthorized, connect.CodeUnauthenticated},
		{server.ErrCodeMethodNotFound, connect.CodeNotFound},
		{server.ErrCodeInternal, connect.CodeInternal},
		{"WAT_IS_THIS", connect.CodeUnknown},
	}
	for _, c := range cases {
		err := frameErrorToConnect("test.method", &server.FrameError{Code: c.legacyCode, Message: "boom"})
		var ce *connect.Error
		if !errors.As(err, &ce) {
			t.Errorf("legacy %q: expected *connect.Error, got %T", c.legacyCode, err)
			continue
		}
		if ce.Code() != c.want {
			t.Errorf("legacy %q: got %v, want %v", c.legacyCode, ce.Code(), c.want)
		}
		if !strings.Contains(ce.Message(), "test.method") || !strings.Contains(ce.Message(), "boom") {
			t.Errorf("legacy %q: error should include method + message, got %q", c.legacyCode, ce.Message())
		}
	}
}

// dispatchJSON ↔ Registry happy path: stand up a fresh Registry,
// register a known handler, dispatch via the bridge, confirm the
// JSON round-trip works.
func TestDispatchJSON_RoundTrip(t *testing.T) {
	reg := server.NewRegistry()
	reg.Register("test.echo", func(_ context.Context, _ server.HandlerCtx, params json.RawMessage) (any, *server.FrameError) {
		return map[string]any{"echoed": string(params)}, nil
	})
	raw, err := dispatchJSON(context.Background(), reg, "test.echo", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("dispatchJSON: %v", err)
	}
	if !strings.Contains(string(raw), `"echoed":"{\"hello\":\"world\"}"`) {
		t.Errorf("unexpected echo round-trip: %s", string(raw))
	}
}

// dispatchJSON with a handler that returns nil — should produce
// JSON null, not an error, so callers that always unmarshal don't
// have to special-case void RPCs.
func TestDispatchJSON_NilResultBecomesNull(t *testing.T) {
	reg := server.NewRegistry()
	reg.Register("test.void", func(_ context.Context, _ server.HandlerCtx, _ json.RawMessage) (any, *server.FrameError) {
		return nil, nil
	})
	raw, err := dispatchJSON(context.Background(), reg, "test.void", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "null" {
		t.Errorf("nil result should marshal as JSON null; got %s", string(raw))
	}
}

// FrameError propagation: handler returns an error → bridge
// translates to the right connect.Code.
func TestDispatchJSON_FrameErrorBecomesConnectError(t *testing.T) {
	reg := server.NewRegistry()
	reg.Register("test.fail", func(_ context.Context, _ server.HandlerCtx, _ json.RawMessage) (any, *server.FrameError) {
		return nil, &server.FrameError{Code: server.ErrCodeBadRequest, Message: "missing field"}
	})
	_, err := dispatchJSON(context.Background(), reg, "test.fail", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *connect.Error, got %T", err)
	}
	if ce.Code() != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want CodeInvalidArgument", ce.Code())
	}
}

// jsonUnmarshalAny tolerates empty strings (void requests have
// no body and the bridge wouldn't want to error on those).
func TestJSONUnmarshalAny_EmptyInput(t *testing.T) {
	var into any
	if err := jsonUnmarshalAny("", &into); err != nil {
		t.Errorf("empty string should not error: %v", err)
	}
	if into != nil {
		t.Errorf("empty input should leave into nil; got %v", into)
	}
}

func TestJSONUnmarshalAny_ValidJSON(t *testing.T) {
	var into any
	if err := jsonUnmarshalAny(`{"k":42}`, &into); err != nil {
		t.Fatal(err)
	}
	m, ok := into.(map[string]any)
	if !ok || m["k"] != float64(42) {
		t.Errorf("decoded shape wrong: %#v", into)
	}
}
