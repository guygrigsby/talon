// Package macopen implements the macOS application-launcher tool as
// a talon plugin library. The subprocess entrypoint is the talon
// binary itself via `talon plugin run mac-open`.
//
// The mac_open tool wraps the macOS `open` command:
//
//	open -a <app> [-b <bundle_id>] [-n] [-g] [<url>] [--args arg...]
//
// Use cases the agent will hit:
//
//   - "Launch Safari"                 → {app: "Safari"}
//   - "Open this URL in Safari"       → {app: "Safari", url: "https://..."}
//   - "Open com.apple.Calculator"     → {bundle_id: "com.apple.Calculator"}
//   - "New Finder window"             → {app: "Finder", new_instance: true}
//   - "Open Calculator in background" → {app: "Calculator", background: true}
//
// Security: every value reaches the OS via exec.Command argv (never
// via `sh -c`), so no shell-injection surface. The agent can launch
// arbitrary applications — that's the contract; trust is enforced
// at the tool-permission layer, not by whitelisting apps here.
package macopen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

type macOpenPlugin struct {
	pb.UnimplementedPluginServer
}

func (s *macOpenPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-mac-open",
			Version:     "0.1.0",
			Description: "Launch macOS applications and open URLs/files in specific apps (Go plugin)",
			OffersTools: []*pb.ToolSpec{{
				Name: "mac_open",
				Description: "Launch a macOS application via the system `open` command. " +
					"Identify the app by name (`Safari`), path (`/Applications/Safari.app`), " +
					"or bundle ID (`com.apple.Safari`). Optionally pass a URL/file to open " +
					"with the app, extra arguments to forward, or flags for a fresh instance " +
					"or background launch. macOS only.",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"app":          {"type": "string", "description": "Application name or path (e.g. \"Safari\", \"/Applications/Safari.app\"). Mutually exclusive with bundle_id; exactly one is required."},
						"bundle_id":    {"type": "string", "description": "Application bundle identifier (e.g. \"com.apple.Safari\"). Mutually exclusive with app; exactly one is required."},
						"url":          {"type": "string", "description": "Optional URL or file path to open with the application. Without an app/bundle_id, the system's default handler for the URL scheme is used."},
						"args":         {"type": "array", "items": {"type": "string"}, "description": "Optional arguments forwarded to the application via --args."},
						"background":   {"type": "boolean", "description": "Launch without bringing the application to the foreground (-g). Default false."},
						"new_instance": {"type": "boolean", "description": "Force a new instance instead of reusing a running one (-n). Default false."}
					},
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

// openArgs is the shape decoded from the tool's arguments JSON. The
// JSON tags mirror the schema names exactly so the agent sees one
// consistent surface.
type openArgs struct {
	App         string   `json:"app"`
	BundleID    string   `json:"bundle_id"`
	URL         string   `json:"url"`
	Args        []string `json:"args"`
	Background  bool     `json:"background"`
	NewInstance bool     `json:"new_instance"`
}

func (s *macOpenPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	if req.GetToolName() != "mac_open" {
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("mac-open plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
	var args openArgs
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return &pb.RunToolResponse{Output: "mac_open: invalid arguments JSON: " + err.Error(), IsError: true}, nil
	}
	argv, err := buildOpenArgs(args)
	if err != nil {
		return &pb.RunToolResponse{Output: "mac_open: " + err.Error(), IsError: true}, nil
	}
	if err := runOpen(ctx, argv); err != nil {
		return &pb.RunToolResponse{Output: "mac_open: " + err.Error(), IsError: true}, nil
	}
	target := args.App
	if target == "" {
		target = args.BundleID
	}
	if args.URL != "" {
		return &pb.RunToolResponse{Output: fmt.Sprintf("opened %s with %s", args.URL, target)}, nil
	}
	return &pb.RunToolResponse{Output: fmt.Sprintf("launched %s", target)}, nil
}

// buildOpenArgs assembles the argv passed to `open`. Split out as a
// pure function so the test suite can validate flag order and
// validation logic without invoking the real command.
func buildOpenArgs(args openArgs) ([]string, error) {
	app := strings.TrimSpace(args.App)
	bundleID := strings.TrimSpace(args.BundleID)
	if app == "" && bundleID == "" {
		return nil, fmt.Errorf("one of app or bundle_id is required")
	}
	if app != "" && bundleID != "" {
		return nil, fmt.Errorf("app and bundle_id are mutually exclusive — pass one, not both")
	}

	out := []string{}
	if args.NewInstance {
		out = append(out, "-n")
	}
	if args.Background {
		out = append(out, "-g")
	}
	if app != "" {
		out = append(out, "-a", app)
	} else {
		out = append(out, "-b", bundleID)
	}
	if url := strings.TrimSpace(args.URL); url != "" {
		out = append(out, url)
	}
	if len(args.Args) > 0 {
		out = append(out, "--args")
		out = append(out, args.Args...)
	}
	return out, nil
}

func (s *macOpenPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

// New returns a configured mac-open plugin PluginServer.
func New() (pb.PluginServer, error) {
	return &macOpenPlugin{}, nil
}
