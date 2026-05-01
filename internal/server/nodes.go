package server

// node.* RPC stubs. talon today is a single-process gateway with no
// multi-node concept (paired devices, remote runners, etc. are an
// openclaw v1+ feature). The UI calls node.list on every connect and
// logs METHOD_NOT_FOUND when it's missing — annoying noise that
// also leaves the Nodes view stuck on "loading."
//
// Returning an empty list is the correct shape: openclaw's UI filters
// `connected === true` over the nodes array client-side, so an empty
// slice falls through cleanly to the empty-state render. Future work
// (talon-xk1: device pairing) will replace this stub with a real
// multi-node implementation.

import (
	"context"
	"encoding/json"
)

// NodesHandler serves the openclaw node.* RPCs. Today only node.list
// is implemented as an empty-list stub.
type NodesHandler struct{}

func NewNodesHandler() *NodesHandler { return &NodesHandler{} }

func (h *NodesHandler) Register(r *Registry) {
	r.Register("node.list", h.handleList)
}

// handleList returns {nodes: []}. Params are ignored — openclaw's UI
// passes {} but tests pass nil/null so we accept anything parseable.
func (h *NodesHandler) handleList(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	return map[string]any{"nodes": []any{}}, nil
}
