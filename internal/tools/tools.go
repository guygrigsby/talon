// Package tools defines the Tool interface and a Registry that the gateway
// uses to expose callable functions to the LLM during chat. Builtin tools
// (read/write/edit/bash/glob/grep) are workspace-scoped: paths resolve
// against the registry's workspace and any escape attempt is rejected.
//
// Tools are deliberately minimal — Run takes raw JSON input (matching the
// LLM's tool_call.arguments shape) and returns text output. Anything more
// structured (per-tool typed schemas, multi-content results, streaming
// progress) is layered on later.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/guygrigsby/talon/internal/provider"
)

// Tool is one callable function exposed to the model.
type Tool interface {
	// Name is the canonical identifier — must be globally unique within a
	// Registry. Models invoke tools by this name.
	Name() string
	// Description is a one-line human/model summary used as the
	// function.description in OpenAI-shaped tool specs. Keep it short.
	Description() string
	// ParametersSchema is the JSON Schema for the tool's input. Echoed
	// verbatim into the provider request as function.parameters.
	ParametersSchema() json.RawMessage
	// Run executes the tool with the given JSON-encoded input and
	// returns the rendered output (typically text the model will
	// consume). ctx cancellation should be respected.
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry is a name-keyed bundle of tools, scoped to a workspace
// directory. All builtin file/exec tools resolve paths under workspace and
// reject anything that would escape it.
type Registry struct {
	workspace string
	tools     map[string]Tool
}

// New constructs a Registry rooted at workspace and registers the default
// builtin tools (read, write, edit, bash, glob, grep, remember). Pass an
// empty workspace string to skip the builtins (useful for tests that
// want to register their own tools).
func New(workspace string) *Registry {
	r := &Registry{
		workspace: workspace,
		tools:     map[string]Tool{},
	}
	if workspace == "" {
		return r
	}
	r.Register(&readTool{ws: workspace})
	r.Register(&writeTool{ws: workspace})
	r.Register(&editTool{ws: workspace})
	r.Register(&bashTool{ws: workspace})
	r.Register(&globTool{ws: workspace})
	r.Register(&grepTool{ws: workspace})
	r.Register(&rememberTool{ws: workspace})
	return r
}

// NewWithSubagent is New plus the subagent tool wired to runner. Use this
// for the user-facing chat (parent agent); inline subagent invocations
// should keep using New so an agent can't recurse without bound. The
// depth-counter in subagent.go is a second line of defense; using New
// for inline runs keeps the subagent tool out of the registry entirely.
func NewWithSubagent(workspace string, runner SubagentRunner) *Registry {
	r := New(workspace)
	if runner != nil && workspace != "" {
		r.Register(&subagentTool{runner: runner})
	}
	return r
}

// Register adds t to the registry, replacing any prior entry with the same
// name.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Names returns the registered tool names in stable lexical order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// Specs returns the registered tools as provider.ToolSpec, ready to attach
// to a provider.Request. Order matches Names().
func (r *Registry) Specs() []provider.ToolSpec {
	names := r.Names()
	out := make([]provider.ToolSpec, len(names))
	for i, n := range names {
		t := r.tools[n]
		out[i] = provider.ToolSpec{
			Name:             t.Name(),
			Description:      t.Description(),
			ParametersSchema: t.ParametersSchema(),
		}
	}
	return out
}

// Run dispatches to the named tool. Returns ErrUnknownTool if the name is
// not registered.
func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	return t.Run(ctx, input)
}

// Workspace returns the directory all builtin tools resolve paths against.
func (r *Registry) Workspace() string { return r.workspace }

// ErrUnknownTool is returned by Registry.Run when name is not registered.
var ErrUnknownTool = fmt.Errorf("unknown tool")

// resolveInWorkspace turns a tool-supplied path (relative or absolute) into
// an absolute path that's guaranteed to be within workspace, after symlink-
// agnostic Clean. Path-traversal attempts (../..) are rejected.
//
// Symlink-following inside the workspace is allowed. If you want to harden
// against symlink escapes too, swap Clean for filepath.EvalSymlinks here —
// that has its own footguns (resolving non-existent paths) so it's left to
// a later sandbox issue (talon-iod).
func resolveInWorkspace(workspace, p string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("tool registry has no workspace configured")
	}
	cleanWS := filepath.Clean(workspace)
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(cleanWS, p))
	}
	// Reject anything not under workspace. Use filepath.Rel rather than a
	// HasPrefix check so "/foo/bar" against workspace "/foo" doesn't
	// accidentally allow "/foobar".
	rel, err := filepath.Rel(cleanWS, abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q under workspace: %w", p, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", p)
	}
	return abs, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
