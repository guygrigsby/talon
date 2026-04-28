// testplugin is a minimal plugin binary used by integration tests in
// internal/plugin. It refuses to start without the handshake env vars
// (so accidentally executing it standalone fails loudly), opens a gRPC
// listener on a random port, prints the handshake line, and serves a
// stub Plugin service that returns a fixed manifest from Initialize.
//
// Build with:
//
//	go build -o /tmp/talon-testplugin ./internal/plugin/testplugin
//
// or from a test, via testing/exec.go-style helpers.
package main

import (
	"context"
	"fmt"
	"net"
	"os"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"google.golang.org/grpc"
)

const handshakeMagic = "talon.plugin.v1"

type stubPlugin struct {
	pb.UnimplementedPluginServer
}

func (s *stubPlugin) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "testplugin",
			Version:     "0.1.0",
			Description: "Integration-test fixture",
			OffersTools: []*pb.ToolSpec{{
				Name:             "test-echo",
				Description:      "Echo the input back as output. Used by integration tests.",
				ParametersSchema: []byte(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			}},
			OffersProviders: []*pb.ProviderSpec{{
				Name:   "testprov",
				Models: []string{"echo-1"},
			}},
			Needs: []pb.Capability{
				pb.Capability_CAPABILITY_READ_CONFIG,
				pb.Capability_CAPABILITY_LIST_AGENTS,
			},
		},
	}, nil
}

// StreamCompletion implements the testprov "echo-1" model: emits two
// text deltas spelling out the last user message, then a final usage
// delta. Used by ToolRouter / PluginProvider integration tests.
func (s *stubPlugin) StreamCompletion(req *pb.StreamCompletionRequest, stream pb.Plugin_StreamCompletionServer) error {
	last := ""
	for _, m := range req.GetMessages() {
		if m.GetRole() == pb.Role_ROLE_USER {
			last = m.GetContent()
		}
	}
	if err := stream.Send(&pb.Delta{Kind: &pb.Delta_Text{Text: "echo: "}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.Delta{Kind: &pb.Delta_Text{Text: last}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.Delta{Kind: &pb.Delta_Usage{Usage: &pb.Usage{
		InputTokens:  int32(len(last)),
		OutputTokens: int32(len(last) + 6),
	}}}); err != nil {
		return err
	}
	return nil
}

// RunTool implements the test-echo tool. Used by ToolRouter integration
// tests; just echoes back whatever JSON was passed in so the test can
// verify the round-trip end-to-end.
func (s *stubPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	if req.GetToolName() != "test-echo" {
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("unknown tool: %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
	return &pb.RunToolResponse{
		Output: fmt.Sprintf("echo: %s (agent=%s run=%s)",
			req.GetArgumentsJson(), req.GetAgentId(), req.GetRunId()),
	}, nil
}

func (s *stubPlugin) Shutdown(ctx context.Context, req *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	// Don't os.Exit here — let the gRPC server return cleanly so the
	// caller's Wait() observes a normal exit. The host's Stop() then
	// kills the process if it lingers.
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

func main() {
	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != handshakeMagic {
		fmt.Fprintf(os.Stderr, "testplugin: TALON_PLUGIN_HANDSHAKE=%q, want %q (refusing to start outside the host)\n", got, handshakeMagic)
		os.Exit(1)
	}
	if os.Getenv("TALON_PLUGIN_AUTH_COOKIE") == "" {
		fmt.Fprintln(os.Stderr, "testplugin: missing TALON_PLUGIN_AUTH_COOKIE")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testplugin: listen: %v\n", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	pb.RegisterPluginServer(server, &stubPlugin{})

	// Print the handshake BEFORE Serve blocks. Stdout is line-buffered
	// when attached to a pipe, so this gets flushed immediately.
	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := server.Serve(listener); err != nil {
		fmt.Fprintf(os.Stderr, "testplugin: serve: %v\n", err)
		os.Exit(1)
	}
}
