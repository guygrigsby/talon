package server

import (
	"context"
	"encoding/json"
	"testing"
)

// TestNodeList_ReturnsEmptyList covers the UI's node.list call on every
// connect. Talon is single-process and has no remote-node concept today,
// so an empty {nodes: []} is the correct response; the UI's filter falls
// through cleanly and no error toast fires.
func TestNodeList_ReturnsEmptyList(t *testing.T) {
	r := NewRegistry()
	NewNodesHandler().Register(r)

	res, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "node.list", json.RawMessage(`{}`))
	if ferr != nil {
		t.Fatalf("node.list dispatch errored: %+v", ferr)
	}
	got, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("response is not a map: %#v", res)
	}
	nodes, ok := got["nodes"].([]any)
	if !ok {
		t.Fatalf("response.nodes is not an array: %#v", got)
	}
	if len(nodes) != 0 {
		t.Errorf("expected empty nodes slice, got %d entries: %#v", len(nodes), nodes)
	}
}

// TestNodeList_AcceptsNilParams is a minor robustness check. Clients
// sending `{}`, an empty body, or `null` should get a clean response.
func TestNodeList_AcceptsNilParams(t *testing.T) {
	r := NewRegistry()
	NewNodesHandler().Register(r)

	for _, params := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(`{}`)} {
		_, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "node.list", params)
		if ferr != nil {
			t.Errorf("params=%q failed: %+v", string(params), ferr)
		}
	}
}
