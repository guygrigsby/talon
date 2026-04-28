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
			Needs: []pb.Capability{
				pb.Capability_CAPABILITY_READ_CONFIG,
				pb.Capability_CAPABILITY_LIST_AGENTS,
			},
		},
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
