package server

// node.* RPC stubs. Talon today is a single-process gateway with no
// multi-node concept. The UI calls node.list on every connect and logs
// METHOD_NOT_FOUND when it's missing, which leaves the Nodes view stuck
// on "loading."
//
// Returning an empty list is the correct shape: the UI filters
// `connected === true` over the nodes array client-side, so an empty slice
// falls through cleanly to the empty-state render.

import (
	"context"
	"encoding/json"
)

// NodesHandler serves the node.* RPCs. Today only node.list is implemented
// as an empty-list stub.
type NodesHandler struct{}

func NewNodesHandler() *NodesHandler { return &NodesHandler{} }

func (h *NodesHandler) Register(r *Registry) {
	r.Register("node.list", h.handleList)
}

// handleList returns {nodes: []}. Params are ignored; tests pass nil/null
// so we accept anything parseable.
func (h *NodesHandler) handleList(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	return map[string]any{"nodes": []any{}}, nil
}
