package native_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/plugin/legacy"
	"github.com/guygrigsby/talon/internal/plugin/native"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// stubHostServer is a tiny pb.HostServer for the integration test.
// Only GetConfig is exercised by the testplugin; other methods stay
// unimplemented (return Unimplemented status from the embedded
// UnimplementedHostServer).
type stubHostServer struct {
	pb.UnimplementedHostServer
	plugin string
}

func (s *stubHostServer) GetConfig(_ context.Context, req *pb.GetConfigRequest) (*pb.GetConfigResponse, error) {
	return &pb.GetConfigResponse{
		RawJson: []byte(`{"plugin":"` + s.plugin + `","path":"` + req.GetPath() + `"}`),
	}, nil
}

// TestNativeSpawn_RoundTrip drives the full bidi flow: spawn the
// testplugin binary via go-plugin, exercise Initialize (which tries
// a denied init-time GetConfig), then RunTool (which exercises a
// post-Initialize allowed GetConfig), then Stop.
//
// Validates: handshake, AutoMTLS, GRPCBroker bidi, manifest-holder
// post-Initialize swap behavior (init call denied, post-init call
// allowed), and lifecycle Unregister via Stop.
func TestNativeSpawn_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("requires go-build of testplugin")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "testplugin")
	out, err := exec.Command("go", "build", "-o", binPath, "./testplugin").CombinedOutput()
	if err != nil {
		t.Fatalf("build testplugin: %v\n%s", err, out)
	}

	host := legacy.NewHost("")
	t.Cleanup(host.Shutdown)

	factory := func(name string, _ *native.ManifestHolder) pb.HostServer {
		return &stubHostServer{plugin: name}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := native.Spawn(ctx, "testplugin", factory,
		native.LoadOptions{Cmd: []string{binPath}}, host.Unregister)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := host.RegisterInstance(inst); err != nil {
		inst.Stop()
		t.Fatalf("RegisterInstance: %v", err)
	}

	if inst.Manifest.GetName() != "testplugin" {
		t.Errorf("manifest.name = %q, want testplugin", inst.Manifest.GetName())
	}

	resp, err := inst.Client.RunTool(ctx, &pb.RunToolRequest{
		ToolName: "echo", ArgumentsJson: `{"x":1}`,
	})
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	// bootConfig should be empty (the init-time GetConfig is denied
	// because the boot manifest is empty); postConfig should carry
	// the stub HostServer's payload.
	if !strings.Contains(resp.Output, `bootConfig=|`) {
		t.Errorf("bootConfig should be empty (init denied); got %q", resp.Output)
	}
	if !strings.Contains(resp.Output, `postConfig={"plugin":"testplugin","path":"testplugin"}`) {
		t.Errorf("postConfig missing or wrong; got %q", resp.Output)
	}

	// Lifecycle: Stop triggers the onExit watcher to call
	// host.Unregister within a tick (1s cadence).
	inst.Stop()
	deadline := time.After(5 * time.Second)
	for host.Get("testplugin") != nil {
		select {
		case <-deadline:
			t.Fatal("Unregister did not run within 5s of Stop")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}
