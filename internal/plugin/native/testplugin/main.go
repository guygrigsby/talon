// Package main is the native plugin host integration test fixture.
// Echoes RunTool input back; calls GetConfig during Initialize to
// exercise the broker round-trip.
package main

import (
	"context"
	"fmt"

	"github.com/guygrigsby/talon/internal/plugin/native"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

type fixture struct {
	pb.UnimplementedPluginServer
	*native.HostClientHolder
	bootConfig []byte
}

func (f *fixture) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	manifest := &pb.Manifest{
		Name:    "testplugin",
		Version: "0.1.0",
		Needs:   []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG},
	}
	if err := f.HostClientHolder.SetFromBroker(req.GetHostBrokerId()); err != nil {
		return nil, fmt.Errorf("testplugin: SetFromBroker: %w", err)
	}
	// Init-time callback uses the boot manifest's capability set.
	// The boot manifest is empty (LoadPlugin sets it that way) so this
	// SHOULD be denied. We try anyway and tolerate the rejection — the
	// test asserts both the boot denial AND the post-Initialize allow.
	resp, err := f.HostClientHolder.Get().GetConfig(ctx, &pb.GetConfigRequest{Path: "testplugin"})
	if err != nil {
		// Expected on the init path; remember nil for the test.
		f.bootConfig = nil
	} else {
		f.bootConfig = resp.GetRawJson()
	}
	return &pb.InitializeResponse{Manifest: manifest}, nil
}

func (f *fixture) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	// Post-Initialize: the host has swapped in the real manifest with
	// READ_CONFIG, so this call should succeed.
	resp, err := f.HostClientHolder.Get().GetConfig(ctx, &pb.GetConfigRequest{Path: "testplugin"})
	if err != nil {
		return nil, fmt.Errorf("testplugin: post-init GetConfig: %w", err)
	}
	return &pb.RunToolResponse{
		Output: fmt.Sprintf("echo:%s|bootConfig=%s|postConfig=%s",
			req.GetArgumentsJson(), string(f.bootConfig), string(resp.GetRawJson())),
	}, nil
}

func (f *fixture) Shutdown(context.Context, *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}

func main() {
	native.Serve("testplugin", &fixture{HostClientHolder: &native.HostClientHolder{}})
}
