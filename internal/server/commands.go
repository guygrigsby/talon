package server

// commands.list — enumerate slash commands the server can surface
// to the UI's command palette beyond the UI's hardcoded set.
//
// Talon's three command sources today are all empty:
//
//   - Native: talon has no server-side command registry. The UI's
//     built-in commands (/help, /clear, /model, …) are defined
//     client-side and reach the palette via the UI's local list.
//   - Skills: skills runtime is unimplemented (talon-ced).
//   - Plugins: the talon plugin manifest exposes offers_tools /
//     offers_providers / offers_channels but no commands field —
//     plugins can't declare slash commands. When that changes the
//     handler grows to walk PluginHost manifests.
//
// Empty {commands: []} is therefore the *correct* current answer,
// not a deferred stub. The UI merges [...local, ...remote] and
// de-dupes by name, so an empty remote slice leaves the built-in
// palette intact. Tracked as talon-18k.

import (
	"context"
	"encoding/json"
)

// CommandsHandler serves the commands.* RPCs. Today only commands.list is
// implemented, as an empty-list stub.
type CommandsHandler struct{}

func NewCommandsHandler() *CommandsHandler { return &CommandsHandler{} }

func (h *CommandsHandler) Register(r *Registry) {
	r.Register("commands.list", h.handleList)
}

// handleList returns {commands: []}. Params (agentId/provider/scope/
// includeArgs) are accepted but ignored — there's nothing to filter.
func (h *CommandsHandler) handleList(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	return map[string]any{"commands": []any{}}, nil
}
