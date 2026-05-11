package pluginrun

import (
	"fmt"
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// HandshakeMagic is the env value the host sets on plugin subprocesses.
// Plugins check it on startup to refuse bare invocations.
const HandshakeMagic = "talon.plugin.v1"

// Serve runs the standard plugin subprocess lifecycle. It validates the
// host handshake env vars, starts a TCP listener, registers srv, prints
// the handshake line to stdout, and calls gRPC Serve. Calls os.Exit on
// fatal errors — this is a subprocess entrypoint, not a library.
func Serve(name string, srv pb.PluginServer) {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	log := slog.With("plugin", name)

	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != HandshakeMagic {
		log.Error("handshake env mismatch — refusing to start outside the host",
			"got", got, "want", HandshakeMagic)
		os.Exit(1)
	}
	if os.Getenv("TALON_PLUGIN_AUTH_COOKIE") == "" {
		log.Error("missing TALON_PLUGIN_AUTH_COOKIE")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	pb.RegisterPluginServer(server, srv)

	// Handshake stays on stdout — host's readHandshake parses the
	// first stdout line literally.
	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := server.Serve(listener); err != nil {
		log.Error("grpc serve failed", "err", err)
		os.Exit(1)
	}
}
