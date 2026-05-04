// talon-mac-notify-plugin is a Go subprocess plugin offering a
// `mac_notify` tool that posts a notification to the local macOS
// Notification Center. Useful for surfacing agent-discovered events
// while the user is at their Mac.
//
// Important UX note: macOS local notifications are LOCAL — they do
// not sync to your iPhone. To deliver to a phone, compose this tool
// with telegram_send (or any other push-channel tool talon ships).
//
// Implementation: shells out to /usr/bin/osascript with the
// `display notification` AppleScript expression. User-supplied data
// (title, body, subtitle) is passed via env vars and read back inside
// the script with `system attribute`, so no AppleScript-injection
// surface — the script is a constant. The notification appears with
// "Script Editor" as the source app, since osascript runs there;
// custom-app-icon notifications would require a code-signed bundled
// helper app, deferred.
//
// Wire shape:
//
//   - Refuses to start without TALON_PLUGIN_HANDSHAKE +
//     TALON_PLUGIN_AUTH_COOKIE.
//   - Listens on 127.0.0.1:0, prints "1|TCP|<addr>|grpc" on stdout.
//   - Initialize: offers_tools=[{name: "mac_notify", schema: {title,
//     body, subtitle?, sound?}}].
//   - RunTool("mac_notify", args): runs osascript, returns "ok" or
//     surfaces the osascript error in is_error=true.
//
// Cross-OS: the gateway uses spawn-time path resolution to find this
// binary, so platforms without a Mac notification surface (Linux
// containers, etc.) just see RunTool fail with a clear "macOS only"
// message — the binary itself runs anywhere but mac_notify is a
// no-op outside darwin. See postNotification's build-tagged files.
//
// Build:
//
//	go build -o bin/talon-mac-notify-plugin ./apps/talon-mac-notify-plugin
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

const handshakeMagic = "talon.plugin.v1"

type macNotifyPlugin struct {
	pb.UnimplementedPluginServer
}

func (s *macNotifyPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-mac-notify",
			Version:     "0.1.0",
			Description: "Local macOS notifications (Go plugin)",
			OffersTools: []*pb.ToolSpec{{
				Name: "mac_notify",
				Description: "Post a notification to the local macOS Notification Center. " +
					"LOCAL ONLY — does not reach your phone. Compose with telegram_send " +
					"when you want both a Mac banner and a phone push. Useful for " +
					"surfacing things you want to see while at the Mac (build done, " +
					"alert fired, doc indexed, etc.).",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"title":    {"type": "string", "description": "Banner title (bold)."},
						"body":     {"type": "string", "description": "Notification body text."},
						"subtitle": {"type": "string", "description": "Optional subtitle line under the title."},
						"sound":    {"type": "string", "description": "Optional sound name (e.g. Glass, Ping, Submarine — see /System/Library/Sounds). Empty = silent."}
					},
					"required": ["title", "body"],
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

func (s *macNotifyPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	if req.GetToolName() != "mac_notify" {
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("mac-notify plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
	var args struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Subtitle string `json:"subtitle"`
		Sound    string `json:"sound"`
	}
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return &pb.RunToolResponse{Output: "mac_notify: invalid arguments JSON: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Title) == "" {
		return &pb.RunToolResponse{Output: "mac_notify: title is required", IsError: true}, nil
	}
	if strings.TrimSpace(args.Body) == "" {
		return &pb.RunToolResponse{Output: "mac_notify: body is required", IsError: true}, nil
	}
	if err := postNotification(ctx, args.Title, args.Body, args.Subtitle, args.Sound); err != nil {
		return &pb.RunToolResponse{Output: "mac_notify: " + err.Error(), IsError: true}, nil
	}
	return &pb.RunToolResponse{Output: "ok"}, nil
}

func (s *macNotifyPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

func main() {
	// Init slog before anything else so handshake / listen errors
	// flow through the same handler as runtime events. Plugins
	// inherit the host's stderr; the host's logger and ours share
	// stderr, structured pairs read uniformly across them.
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	log := slog.With("plugin", "mac-notify")

	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != handshakeMagic {
		log.Error("handshake env mismatch — refusing to start outside the host",
			"got", got, "want", handshakeMagic)
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

	srv := grpc.NewServer()
	pb.RegisterPluginServer(srv, &macNotifyPlugin{})

	// Handshake line still goes to stdout — the host's
	// readHandshake parses the FIRST stdout line literally and
	// must not see slog formatting. Stays as fmt.Printf.
	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := srv.Serve(listener); err != nil {
		log.Error("grpc serve failed", "err", err)
		os.Exit(1)
	}
}
