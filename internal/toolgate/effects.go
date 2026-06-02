// Package toolgate classifies and gates every agent tool call on the live
// chatdriver path using pinion (ADR 0017). pinion is the classifier; talon
// remains the executor. There is no anti-corruption layer: toolgate imports
// pinion's effect/analyze/compose/policy types directly, because pinion is a
// first-party dependency that co-evolves with talon.
//
// effects.go holds the effect-mapping registry: the table that turns a tool
// call (name + raw JSON args) into the pinion effects it exercises, with scopes
// resolved against the agent workspace.
package toolgate

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/guygrigsby/pinion/effect"
)

// EffectsFor maps a tool call to the pinion effects it exercises, scoped to the
// agent workspace. It is the core artifact of ADR 0017 and is conservative by
// construction:
//
//   - read/grep/glob/ls/claude_memory -> fs.read (scope from the path arg)
//   - write/edit                      -> fs.write (scope from file_path)
//   - bash                            -> exec (a sink; an arbitrary command
//     string can't be scoped, so bash is treated as high-danger)
//   - unknown / plugin-undeclared     -> effect.MaxDanger (fail-closed: every
//     source and sink, so any taint flow through it lights up)
//
// Scope extraction is best-effort: an absolute path is used as-is, a relative
// path is resolved against the workspace, a missing path falls back to the
// per-tool default, and unparseable args widen the effect to unscoped (the
// broadest request) — which can only tighten the eventual verdict.
func EffectsFor(name string, args json.RawMessage, workspace string) []effect.Effect {
	switch name {
	case "read":
		// A read with no file_path is malformed; leave it unscoped (denied by a
		// scoped grant) rather than guessing the whole workspace.
		return []effect.Effect{{Kind: effect.FSRead, Scope: pathScope(args, "file_path", workspace, "")}}
	case "grep", "glob", "ls":
		// These default to searching the working directory (the workspace) when
		// no path is given, so an omitted path scopes to the workspace glob —
		// which the workspace grant covers, keeping normal searches allowed.
		return []effect.Effect{{Kind: effect.FSRead, Scope: pathScope(args, "path", workspace, workspaceGlob(workspace))}}
	case "claude_memory":
		// talon's notes store lives outside the agent workspace; an unscoped
		// read keeps the workspace grant from silently covering it.
		return []effect.Effect{{Kind: effect.FSRead}}
	case "write", "edit":
		return []effect.Effect{{Kind: effect.FSWrite, Scope: pathScope(args, "file_path", workspace, "")}}
	case "bash":
		return []effect.Effect{{Kind: effect.Exec}}
	default:
		return effect.MaxDanger()
	}
}

// pathScope extracts a path-valued arg and resolves it to a scope. Unparseable
// or empty args yield the widest (unscoped) request; a present-but-empty field
// falls back to absentDefault; a present field is resolved against workspace.
func pathScope(args json.RawMessage, key, workspace, absentDefault string) effect.Scope {
	if len(args) == 0 {
		return effect.Scope{}
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return effect.Scope{}
	}
	p, _ := m[key].(string)
	if p == "" {
		return effect.Scope{Pattern: absentDefault}
	}
	return effect.Scope{Pattern: resolvePath(p, workspace)}
}

// resolvePath returns an absolute-ish scope pattern: absolute paths pass
// through; relative paths join the workspace. An empty workspace leaves the
// path as given.
func resolvePath(p, workspace string) string {
	if workspace == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workspace, p)
}

// workspaceGlob is the recursive glob covering everything under the workspace,
// matching the workspace-scoped grant pattern.
func workspaceGlob(workspace string) string {
	if workspace == "" {
		return ""
	}
	return strings.TrimRight(workspace, "/") + "/**"
}
