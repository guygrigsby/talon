package server

import (
	"context"
	"encoding/json"
	"testing"
)

// TestCommandsList_ReturnsEmptyList covers talon-18k: openclaw's UI
// calls commands.list on every connect to populate slash-commands.
// talon has no skill/plugin command surface yet (talon-ced, talon-aub),
// so an empty {commands: []} is the correct response — the UI's
// [...local, ...remote] merge keeps its built-in commands and no
// error toast fires.
func TestCommandsList_ReturnsEmptyList(t *testing.T) {
	r := NewRegistry()
	NewCommandsHandler().Register(r)

	res, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "commands.list", json.RawMessage(`{}`))
	if ferr != nil {
		t.Fatalf("commands.list dispatch errored: %+v", ferr)
	}
	got, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("response is not a map: %#v", res)
	}
	cmds, ok := got["commands"].([]any)
	if !ok {
		t.Fatalf("response.commands is not an array: %#v", got)
	}
	if len(cmds) != 0 {
		t.Errorf("expected empty commands slice, got %d entries: %#v", len(cmds), cmds)
	}
}

// TestCommandsList_AcceptsVariedParams confirms the handler accepts
// the param shapes openclaw's UI actually sends: {} on a bare call,
// {scope, includeArgs} from slash-commands.ts, plus null/nil bodies
// for robustness against future callers.
func TestCommandsList_AcceptsVariedParams(t *testing.T) {
	r := NewRegistry()
	NewCommandsHandler().Register(r)

	cases := []json.RawMessage{
		nil,
		json.RawMessage("null"),
		json.RawMessage(`{}`),
		json.RawMessage(`{"scope":"text","includeArgs":true}`),
		json.RawMessage(`{"agentId":"main","provider":"anthropic","scope":"both","includeArgs":false}`),
	}
	for _, params := range cases {
		_, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "commands.list", params)
		if ferr != nil {
			t.Errorf("params=%q failed: %+v", string(params), ferr)
		}
	}
}
