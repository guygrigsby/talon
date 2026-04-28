package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/tools"
)

// LocalRunner is the structural-typing surface ToolRunner consumers need.
// Mirror of server.ToolRunner; declared here so internal/plugin doesn't
// have to import internal/server. Any *tools.Registry satisfies this.
type LocalRunner interface {
	Run(ctx context.Context, name string, input json.RawMessage) (string, error)
	Specs() []provider.ToolSpec
}

// ToolRouter unions a local tool runner (builtins + subagent) with the
// tools advertised by every currently-registered plugin. The chat
// handler hands a ToolRouter to provider.Request.Tools so the model
// sees both — the router routes a model-issued tool call to whichever
// side advertises that name. Local builtins win on collision.
//
// Plugin tools dispatch via Plugin.RunTool over gRPC; the response's
// is_error flag is surfaced as a Go error so the multi-turn loop can
// capture it as the tool result string.
type ToolRouter struct {
	base LocalRunner
	host *Host
}

// NewToolRouter constructs a router. host may be nil — the router
// degrades to base-only and adding plugins later requires recreating
// (per chat.send the gateway constructs a fresh router so this matches
// the natural lifecycle).
func NewToolRouter(base LocalRunner, host *Host) *ToolRouter {
	return &ToolRouter{base: base, host: host}
}

// Specs returns the union of base and plugin tool specs. Local builtins
// come first so collision-shadowed plugin tools still appear (the model
// won't be able to invoke them, but listing both keeps the manifest
// honest for diagnostic output).
func (r *ToolRouter) Specs() []provider.ToolSpec {
	out := r.base.Specs()
	if r.host == nil {
		return out
	}
	for _, name := range r.host.List() {
		inst := r.host.Get(name)
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ts := range inst.Manifest.OffersTools {
			out = append(out, provider.ToolSpec{
				Name:             ts.Name,
				Description:      ts.Description,
				ParametersSchema: json.RawMessage(ts.ParametersSchema),
			})
		}
	}
	return out
}

// Run dispatches a tool invocation. Local builtins first; if base
// returns ErrUnknownTool, walk plugins looking for the name.
func (r *ToolRouter) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	out, err := r.base.Run(ctx, name, input)
	if err == nil || !errors.Is(err, tools.ErrUnknownTool) {
		return out, err
	}
	if r.host == nil {
		return "", err
	}
	inst := r.lookupPluginTool(name)
	if inst == nil {
		// Preserve the local error so the model gets the standard
		// "unknown tool" message regardless of where the tool would
		// have come from.
		return "", err
	}
	return r.invokePluginTool(ctx, inst, name, input)
}

func (r *ToolRouter) lookupPluginTool(name string) *Instance {
	for _, pname := range r.host.List() {
		inst := r.host.Get(pname)
		if inst == nil || inst.Manifest == nil {
			continue
		}
		for _, ts := range inst.Manifest.OffersTools {
			if ts.Name == name {
				return inst
			}
		}
	}
	return nil
}

func (r *ToolRouter) invokePluginTool(ctx context.Context, inst *Instance, name string, input json.RawMessage) (string, error) {
	agentID := AgentIDFromContext(ctx)
	runID := RunIDFromContext(ctx)
	resp, err := inst.Client.RunTool(ctx, &pb.RunToolRequest{
		ToolName:      name,
		ArgumentsJson: string(input),
		AgentId:       agentID,
		RunId:         runID,
	})
	if err != nil {
		return "", fmt.Errorf("plugin %s: %w", inst.Name, err)
	}
	if resp.GetIsError() {
		// Tool ran but the operation failed at the model-visible
		// level. Surface as a Go error AND the output text — the
		// caller can capture both into the tool result.
		body := strings.TrimSpace(resp.GetOutput())
		if body == "" {
			body = "tool error"
		}
		return resp.GetOutput(), fmt.Errorf("plugin %s tool %s: %s", inst.Name, name, body)
	}
	return resp.GetOutput(), nil
}

// =============================================================================
// Context propagation
// =============================================================================
//
// Tool runs need to know which agent and run they're servicing so plugins
// can scope their behavior (per-agent grants, log correlation, multi-tenant
// state). The chat handler stuffs both onto ctx before invoking
// runner.Run; the router pulls them off when forwarding to a plugin.

type agentIDKey struct{}
type runIDKey struct{}

// WithAgentID stamps the agent id onto ctx for downstream tool calls.
func WithAgentID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, agentIDKey{}, id)
}

// AgentIDFromContext returns the agent id stamped by WithAgentID, or ""
// if the ctx wasn't decorated.
func AgentIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(agentIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRunID stamps the chat run id onto ctx.
func WithRunID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunIDFromContext returns the run id stamped by WithRunID, or "" if
// absent.
func RunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey{}).(string); ok {
		return v
	}
	return ""
}
