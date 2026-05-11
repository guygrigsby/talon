// Package macnotify implements the macOS notification tool as a talon plugin library.
// The subprocess entrypoint (apps/talon-mac-notify-plugin/main.go) calls New()
// and pluginrun.Serve() to wire it up.
package macnotify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

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

// New returns a configured mac-notify plugin PluginServer.
func New() (pb.PluginServer, error) {
	return &macNotifyPlugin{}, nil
}
