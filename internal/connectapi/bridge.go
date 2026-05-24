// Package connectapi adapts talon's existing JSON-RPC registry to
// the Connect HTTP/gRPC surface defined under internal/api/v1.
// Lives in its own package so the generated proto types and the
// hand-written handler glue don't tangle.
//
// Why this exists: the WS path uses server.Registry — a map of
// method-name → JSON-in/JSON-out Handler. The Connect path has
// typed messages. Rather than duplicate handler logic in two
// places during the migration, every Connect handler here is a
// thin shell that:
//
//  1. Marshals the typed Connect request into the same JSON shape
//     the WS handler consumes.
//  2. Calls registry.Dispatch with the legacy method name.
//  3. Re-marshals the registry's any return into the typed Connect
//     response.
//
// When the WS path is finally retired (talon-y6v), handlers can
// be lifted directly out of internal/server/* into typed
// implementations here. Until then, one source of truth: the
// existing Registry.
package connectapi

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/guygrigsby/talon/internal/server"
)

// dispatchJSON runs a registry handler with the given JSON params
// and returns the response as raw JSON bytes. Most Connect adapter
// methods boil down to one call to this. The HandlerCtx is empty
// for now — none of the unary handlers we're bridging today read
// session state from it; chat.send / sessions.subscribe will need
// special-casing once those stream methods land.
func dispatchJSON(ctx context.Context, reg *server.Registry, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("%s: marshal request: %w", method, err))
		}
		raw = b
	}
	result, frameErr := reg.Dispatch(ctx, server.HandlerCtx{}, method, raw)
	if frameErr != nil {
		return nil, frameErrorToConnect(method, frameErr)
	}
	if result == nil {
		// Empty body is a valid response for void RPCs (Forget,
		// Patch, etc). Return a JSON null so callers that always
		// unmarshal don't choke.
		return json.RawMessage("null"), nil
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("%s: marshal response: %w", method, err))
	}
	return out, nil
}

// dispatchInto runs a registry handler and unmarshals the JSON
// result into the typed Connect response. Convenience wrapper
// around dispatchJSON for the common typed-response case.
func dispatchInto(ctx context.Context, reg *server.Registry, method string, params any, into any) error {
	raw, err := dispatchJSON(ctx, reg, method, params)
	if err != nil {
		return err
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return connect.NewError(connect.CodeInternal,
			fmt.Errorf("%s: unmarshal response: %w", method, err))
	}
	return nil
}

// frameErrorToConnect translates the legacy FrameError code into a
// Connect status code so HTTP clients see a meaningful status
// (400 for bad request, 401 for unauthorized, 404 for unknown
// method, etc) instead of generic 500s.
func frameErrorToConnect(method string, fe *server.FrameError) error {
	code := connect.CodeUnknown
	switch fe.Code {
	case server.ErrCodeBadRequest:
		code = connect.CodeInvalidArgument
	case server.ErrCodeUnauthorized:
		code = connect.CodeUnauthenticated
	case server.ErrCodeMethodNotFound:
		code = connect.CodeNotFound
	case server.ErrCodeInternal:
		code = connect.CodeInternal
	}
	return connect.NewError(code, fmt.Errorf("%s: %s", method, fe.Message))
}

// jsonUnmarshalAny is a tiny helper for adapter methods that pass
// a request's raw JSON body straight to a Registry handler whose
// param shape is too dynamic to declare in proto (e.g. cron.add
// where different job kinds carry different fields).
//
// Takes string (matching the JSONPayload.json field type — see
// common.proto for the bytes-vs-string rationale).
func jsonUnmarshalAny(raw string, into *any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}
