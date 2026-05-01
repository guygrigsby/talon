// talon-deepseek-plugin is a Go subprocess plugin serving the DeepSeek
// provider through talon's gRPC plugin protocol. It replaces the native
// in-tree DeepSeek path (internal/provider/deepseek) with a plugin
// boundary so the same DeepSeek implementation can be unloaded,
// upgraded, or sandboxed without touching the gateway binary.
//
// Wire shape:
//
//   - Refuses to start without TALON_PLUGIN_HANDSHAKE +
//     TALON_PLUGIN_AUTH_COOKIE (set by the host on spawn). Bare
//     invocations exit with a clear error.
//   - Reads the DeepSeek API key from the openclaw-style auth-profiles
//     at $HOME/.openclaw/agents/main/agent/auth-profiles.json under
//     "deepseek:default". This is the same path/profile the host's
//     agentProviderFactory uses, so existing config carries over with
//     no changes.
//   - Listens on 127.0.0.1:0, prints "1|TCP|<addr>|grpc" on stdout
//     for the host's handshake parser, and serves the Plugin gRPC
//     service.
//   - Initialize advertises offers_providers=[{name: "deepseek",
//     models: [...]}] with a needs list that includes
//     CAPABILITY_READ_CONFIG (reserved for future host callbacks; we
//     don't make any today since the API key is read from the file
//     directly).
//   - StreamCompletion delegates to internal/provider/openai's
//     Provider configured for DeepSeek's base URL, then adapts each
//     emitted provider.Delta to a pb.Delta and writes it to the
//     gRPC stream.
//
// Build:
//
//	go build -o /tmp/talon-deepseek ./apps/talon-deepseek-plugin
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/provider/deepseek"
	"github.com/guygrigsby/talon/internal/provider/openai"
)

const handshakeMagic = "talon.plugin.v1"

// deepseekModels is the manifest's advertised model list. Kept in sync
// with what the openclaw catalog ships for DeepSeek so the gateway's
// model resolver can route "deepseek/<id>" without extra config.
var deepseekModels = []string{
	"deepseek-chat",
	"deepseek-reasoner",
}

type deepseekPlugin struct {
	pb.UnimplementedPluginServer
	prov *openai.Provider
}

func (s *deepseekPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-deepseek",
			Version:     "0.1.0",
			Description: "DeepSeek chat-completions provider (Go plugin)",
			OffersProviders: []*pb.ProviderSpec{{
				Name:   "deepseek",
				Models: deepseekModels,
			}},
			// We don't actually call back into the host today — the API
			// key is read directly from auth-profiles.json on startup.
			// Listing the capability anyway keeps the option open for
			// future host-config-driven model lists.
			Needs: []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG},
		},
	}, nil
}

func (s *deepseekPlugin) StreamCompletion(req *pb.StreamCompletionRequest, stream pb.Plugin_StreamCompletionServer) error {
	if s.prov == nil {
		return errors.New("deepseek plugin: provider not initialized")
	}
	preq, err := adaptRequest(req)
	if err != nil {
		return err
	}
	deltaCh, err := s.prov.Stream(stream.Context(), preq)
	if err != nil {
		return err
	}
	for d := range deltaCh {
		out := adaptDelta(d)
		if out == nil {
			continue
		}
		if err := stream.Send(out); err != nil {
			// Drain remaining deltas so the provider goroutine's
			// channel-write doesn't block forever; ignore the values.
			for range deltaCh {
			}
			return err
		}
	}
	return nil
}

func (s *deepseekPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	// Defer the actual exit so the gRPC reply ships before the server
	// stops. The host will follow up with a SIGKILL if the process
	// lingers, mirroring testplugin/main.go.
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

// adaptRequest translates a pb.StreamCompletionRequest into the
// in-tree provider.Request the openai/deepseek implementation expects.
// Field names are snake_case on the wire, camelCase in Go; the model
// is taken verbatim (provider routing already happened on the host
// side, so the prefix is absent here).
func adaptRequest(req *pb.StreamCompletionRequest) (provider.Request, error) {
	out := provider.Request{
		Model:  provider.ModelID("deepseek/" + req.GetModel()),
		System: req.GetSystem(),
	}
	for _, m := range req.GetMessages() {
		out.Messages = append(out.Messages, provider.Message{
			Role:       roleFromProto(m.GetRole()),
			Content:    m.GetContent(),
			ToolCalls:  toolCallsFromProto(m.GetToolCalls()),
			ToolCallID: m.GetToolCallId(),
		})
	}
	for _, t := range req.GetTools() {
		out.Tools = append(out.Tools, provider.ToolSpec{
			Name:             t.GetName(),
			Description:      t.GetDescription(),
			ParametersSchema: t.GetParametersSchema(),
		})
	}
	if req.Temperature != nil {
		v := req.GetTemperature()
		out.Options.Temperature = &v
	}
	if n := req.GetMaxOutputTokens(); n > 0 {
		out.Options.MaxOutputTokens = int(n)
	}
	return out, nil
}

func roleFromProto(r pb.Role) provider.Role {
	switch r {
	case pb.Role_ROLE_SYSTEM:
		return provider.RoleSystem
	case pb.Role_ROLE_USER:
		return provider.RoleUser
	case pb.Role_ROLE_ASSISTANT:
		return provider.RoleAssistant
	case pb.Role_ROLE_TOOL:
		return provider.RoleTool
	default:
		// Default to user — bad inputs land here. The provider's
		// own validation will flag the role mismatch downstream
		// rather than the plugin shim swallowing it.
		return provider.RoleUser
	}
}

func toolCallsFromProto(in []*pb.ToolCall) []provider.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]provider.ToolCall, 0, len(in))
	for _, c := range in {
		out = append(out, provider.ToolCall{
			ID:            c.GetId(),
			Name:          c.GetName(),
			ArgumentsJSON: c.GetArgumentsJson(),
		})
	}
	return out
}

// adaptDelta translates an in-tree provider.Delta to a pb.Delta. The
// Reasoning kind isn't surfaced by the OpenAI/DeepSeek path today, so
// the switch leaves it falling through to nil — the caller skips
// nil deltas.
func adaptDelta(d provider.Delta) *pb.Delta {
	switch d.Kind {
	case provider.DeltaText:
		return &pb.Delta{Kind: &pb.Delta_Text{Text: d.Text}}
	case provider.DeltaReasoning:
		return &pb.Delta{Kind: &pb.Delta_Reasoning{Reasoning: d.Text}}
	case provider.DeltaUsage:
		if d.Usage == nil {
			return nil
		}
		return &pb.Delta{Kind: &pb.Delta_Usage{Usage: &pb.Usage{
			InputTokens:  int32(d.Usage.InputTokens),
			OutputTokens: int32(d.Usage.OutputTokens),
		}}}
	case provider.DeltaToolCall:
		if d.ToolCall == nil {
			return nil
		}
		return &pb.Delta{Kind: &pb.Delta_ToolCall{ToolCall: &pb.ToolCall{
			Id:            d.ToolCall.ID,
			Name:          d.ToolCall.Name,
			ArgumentsJson: d.ToolCall.ArgumentsJSON,
		}}}
	case provider.DeltaError:
		msg := "stream error"
		if d.Err != nil {
			msg = d.Err.Error()
		}
		return &pb.Delta{Kind: &pb.Delta_Error{Error: msg}}
	}
	return nil
}

// loadAPIKey reads the DeepSeek API key from the openclaw auth-profiles
// the host's user has on disk. Convention matches the in-tree
// LoadAPIKey: profileID="deepseek:default", expectedProvider="deepseek",
// pulled from $HOME/.openclaw/agents/main/agent/auth-profiles.json.
//
// Empty key + nil error means the user hasn't configured DeepSeek.
// We surface that as a clear init-time error rather than letting the
// first StreamCompletion fail.
func loadAPIKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	authPath := filepath.Join(home, ".openclaw", "agents", "main", "agent", "auth-profiles.json")
	key, err := deepseek.LoadAPIKey(authPath)
	if err != nil {
		return "", fmt.Errorf("load %s: %w", authPath, err)
	}
	return key, nil
}

func main() {
	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != handshakeMagic {
		fmt.Fprintf(os.Stderr, "talon-deepseek-plugin: TALON_PLUGIN_HANDSHAKE=%q, want %q (refusing to start outside the host)\n", got, handshakeMagic)
		os.Exit(1)
	}
	if os.Getenv("TALON_PLUGIN_AUTH_COOKIE") == "" {
		fmt.Fprintln(os.Stderr, "talon-deepseek-plugin: missing TALON_PLUGIN_AUTH_COOKIE")
		os.Exit(1)
	}

	apiKey, err := loadAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "talon-deepseek-plugin: %v\n", err)
		os.Exit(1)
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "talon-deepseek-plugin: deepseek:default profile has empty key — configure it in auth-profiles.json before enabling this plugin")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "talon-deepseek-plugin: listen: %v\n", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	pb.RegisterPluginServer(server, &deepseekPlugin{
		prov: deepseek.New(deepseek.Options{APIKey: apiKey}),
	})

	// Print the handshake BEFORE Serve blocks. Stdout is line-buffered
	// when attached to a pipe, so this gets flushed immediately. The
	// host parses the line via internal/plugin/handshake.go.
	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := server.Serve(listener); err != nil {
		fmt.Fprintf(os.Stderr, "talon-deepseek-plugin: serve: %v\n", err)
		os.Exit(1)
	}
}
