package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
)

// PluginProvider adapts a pb.PluginClient (one whose manifest offers a
// provider) to the provider.Provider interface so the chat handler's
// existing routing flow doesn't need plugin-specific branches. Stream
// translates provider.Request to pb.StreamCompletionRequest, opens the
// gRPC server-stream, and pumps each pb.Delta back into the chat
// handler's provider.Delta channel via translateDelta.
//
// One PluginProvider per (provider name, plugin client) pair —
// agentProviderFactory constructs a fresh adapter on each chat.send,
// so a plugin reload that updates inst.Client shows up automatically.
type PluginProvider struct {
	name   string
	client pb.PluginClient
}

// NewPluginProvider wires a plugin client behind the named provider.
// name should match the provider key the model's ModelID targets
// (e.g. "weather-llm" for "weather-llm/quick").
func NewPluginProvider(name string, client pb.PluginClient) *PluginProvider {
	return &PluginProvider{name: name, client: client}
}

// Name reports the provider's key, satisfying provider.Provider.
func (p *PluginProvider) Name() string { return p.name }

// Stream forwards req to the plugin's StreamCompletion and translates
// each pb.Delta back to provider.Delta on the returned channel. The
// channel closes when:
//
//   - the gRPC stream EOFs cleanly (assistant turn ended)
//   - the gRPC stream errors mid-flight (DeltaError emitted, then close)
//   - ctx is cancelled (channel closes; receiver should consult ctx.Err)
func (p *PluginProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	if pseg := req.Model.Provider(); pseg != "" && pseg != p.name {
		return nil, fmt.Errorf("plugin %s: model %q does not target this provider", p.name, req.Model)
	}
	pbReq := &pb.StreamCompletionRequest{
		Model:           req.Model.Model(),
		System:          req.System,
		Messages:        messagesToProto(req.Messages),
		Tools:           toolSpecsToProto(req.Tools),
		MaxOutputTokens: int32(req.Options.MaxOutputTokens),
	}
	if req.Options.Temperature != nil {
		t := *req.Options.Temperature
		pbReq.Temperature = &t
	}
	stream, err := p.client.StreamCompletion(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: open stream: %w", p.name, err)
	}

	ch := make(chan provider.Delta)
	go func() {
		defer close(ch)
		for {
			pbDelta, err := stream.Recv()
			if errors.Is(err, io.EOF) || err == io.EOF {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return // caller cancelled; no point flagging error
				}
				select {
				case <-ctx.Done():
				case ch <- provider.Delta{Kind: provider.DeltaError, Err: fmt.Errorf("plugin %s: %w", p.name, err)}:
				}
				return
			}
			d, ok := translateDelta(pbDelta)
			if !ok {
				continue // malformed delta; skip rather than break the stream
			}
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
	return ch, nil
}

// =============================================================================
// translation helpers
// =============================================================================

func messagesToProto(msgs []provider.Message) []*pb.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]*pb.Message, len(msgs))
	for i, m := range msgs {
		out[i] = &pb.Message{
			Role:       roleToProto(m.Role),
			Content:    m.Content,
			ToolCalls:  toolCallsToProto(m.ToolCalls),
			ToolCallId: m.ToolCallID,
		}
	}
	return out
}

func toolSpecsToProto(specs []provider.ToolSpec) []*pb.ToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]*pb.ToolSpec, len(specs))
	for i, s := range specs {
		out[i] = &pb.ToolSpec{
			Name:             s.Name,
			Description:      s.Description,
			ParametersSchema: []byte(s.ParametersSchema),
		}
	}
	return out
}

func toolCallsToProto(calls []provider.ToolCall) []*pb.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]*pb.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = &pb.ToolCall{
			Id:            c.ID,
			Name:          c.Name,
			ArgumentsJson: c.ArgumentsJSON,
		}
	}
	return out
}

func roleToProto(r provider.Role) pb.Role {
	switch r {
	case provider.RoleUser:
		return pb.Role_ROLE_USER
	case provider.RoleAssistant:
		return pb.Role_ROLE_ASSISTANT
	case provider.RoleSystem:
		return pb.Role_ROLE_SYSTEM
	case provider.RoleTool:
		return pb.Role_ROLE_TOOL
	}
	return pb.Role_ROLE_UNSPECIFIED
}

// translateDelta converts an inbound pb.Delta to provider.Delta. Returns
// (zero, false) when the delta has no recognized variant — the caller
// just skips those rather than aborting the stream.
func translateDelta(d *pb.Delta) (provider.Delta, bool) {
	if d == nil {
		return provider.Delta{}, false
	}
	switch v := d.GetKind().(type) {
	case *pb.Delta_Text:
		return provider.Delta{Kind: provider.DeltaText, Text: v.Text}, true
	case *pb.Delta_Reasoning:
		return provider.Delta{Kind: provider.DeltaReasoning, Text: v.Reasoning}, true
	case *pb.Delta_Usage:
		return provider.Delta{
			Kind: provider.DeltaUsage,
			Usage: &provider.Usage{
				InputTokens:     int(v.Usage.GetInputTokens()),
				OutputTokens:    int(v.Usage.GetOutputTokens()),
				ReasoningTokens: int(v.Usage.GetReasoningTokens()),
			},
		}, true
	case *pb.Delta_ToolCall:
		tc := v.ToolCall
		return provider.Delta{
			Kind: provider.DeltaToolCall,
			ToolCall: &provider.ToolCall{
				ID:            tc.GetId(),
				Name:          tc.GetName(),
				ArgumentsJSON: tc.GetArgumentsJson(),
			},
		}, true
	case *pb.Delta_Error:
		return provider.Delta{Kind: provider.DeltaError, Err: errors.New(v.Error)}, true
	}
	return provider.Delta{}, false
}
