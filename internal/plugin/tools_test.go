package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/tools"
	"google.golang.org/grpc"
)

// stubLocal is a minimal LocalRunner: a fixed Specs list + Run that
// returns canned output for known names and ErrUnknownTool otherwise.
type stubLocal struct {
	specs   []provider.ToolSpec
	outputs map[string]string
}

func (s *stubLocal) Specs() []provider.ToolSpec { return s.specs }

func (s *stubLocal) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	if out, ok := s.outputs[name]; ok {
		return out, nil
	}
	return "", tools.ErrUnknownTool
}

// stubPluginClient is an in-process pb.PluginClient for unit tests.
// Records RunTool calls and returns canned responses; the streaming
// methods are stubbed because the router only uses Initialize+RunTool.
type stubPluginClient struct {
	manifest    *pb.Manifest
	runCalls    []*pb.RunToolRequest
	runResponse *pb.RunToolResponse
	runErr      error
}

func (c *stubPluginClient) Initialize(ctx context.Context, req *pb.InitializeRequest, _ ...grpc.CallOption) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{Manifest: c.manifest}, nil
}
func (c *stubPluginClient) Shutdown(ctx context.Context, req *pb.ShutdownRequest, _ ...grpc.CallOption) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}
func (c *stubPluginClient) RunTool(ctx context.Context, req *pb.RunToolRequest, _ ...grpc.CallOption) (*pb.RunToolResponse, error) {
	c.runCalls = append(c.runCalls, req)
	if c.runErr != nil {
		return nil, c.runErr
	}
	return c.runResponse, nil
}
func (c *stubPluginClient) StreamCompletion(context.Context, *pb.StreamCompletionRequest, ...grpc.CallOption) (pb.Plugin_StreamCompletionClient, error) {
	return nil, errors.New("not implemented")
}
func (c *stubPluginClient) StartChannel(context.Context, *pb.StartChannelRequest, ...grpc.CallOption) (pb.Plugin_StartChannelClient, error) {
	return nil, errors.New("not implemented")
}
func (c *stubPluginClient) SendChannelMessage(context.Context, *pb.SendChannelMessageRequest, ...grpc.CallOption) (*pb.SendChannelMessageResponse, error) {
	return nil, errors.New("not implemented")
}

// regHostWith creates a Host with stub registered under "testplug" so
// each test doesn't repeat the wiring boilerplate.
func regHostWith(t *testing.T, stub *stubPluginClient) *Host {
	t.Helper()
	h := NewHost("")
	inst := &Instance{
		Name:     "testplug",
		Cookie:   "test-cookie",
		Manifest: stub.manifest,
		Client:   stub,
	}
	h.byName["testplug"] = inst
	h.byCookie[inst.Cookie] = inst
	return h
}

// --- Specs -----------------------------------------------------------

func TestToolRouter_SpecsUnionsLocalAndPlugin(t *testing.T) {
	local := &stubLocal{specs: []provider.ToolSpec{{Name: "read"}, {Name: "bash"}}}
	stub := &stubPluginClient{manifest: &pb.Manifest{
		Name: "testplug",
		OffersTools: []*pb.ToolSpec{
			{Name: "weather", Description: "current weather", ParametersSchema: []byte(`{"type":"object"}`)},
			{Name: "stocks", Description: "stock quote", ParametersSchema: []byte(`{"type":"object"}`)},
		},
	}}
	r := NewToolRouter(local, regHostWith(t, stub))

	got := r.Specs()
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"read", "bash", "weather", "stocks"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Specs() names = %v, want %v", names, want)
	}
	// Plugin tool's schema came through the wire as bytes and lands as
	// json.RawMessage.
	for _, s := range got {
		if s.Name == "weather" && string(s.ParametersSchema) != `{"type":"object"}` {
			t.Errorf("weather schema didn't roundtrip: %s", s.ParametersSchema)
		}
	}
}

func TestToolRouter_SpecsBaseOnlyWhenHostNil(t *testing.T) {
	local := &stubLocal{specs: []provider.ToolSpec{{Name: "read"}}}
	r := NewToolRouter(local, nil)
	if len(r.Specs()) != 1 {
		t.Errorf("expected base-only specs when host is nil")
	}
}

// --- Run dispatch ----------------------------------------------------

func TestToolRouter_RunPrefersLocal(t *testing.T) {
	// Both local and plugin advertise "shared". Local must win.
	local := &stubLocal{
		specs:   []provider.ToolSpec{{Name: "shared"}},
		outputs: map[string]string{"shared": "from-local"},
	}
	stub := &stubPluginClient{manifest: &pb.Manifest{
		OffersTools: []*pb.ToolSpec{{Name: "shared"}},
	}, runResponse: &pb.RunToolResponse{Output: "from-plugin"}}
	r := NewToolRouter(local, regHostWith(t, stub))

	out, err := r.Run(t.Context(), "shared", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "from-local" {
		t.Errorf("expected local to win, got %q", out)
	}
	if len(stub.runCalls) != 0 {
		t.Errorf("plugin should not have been called when local handles the name")
	}
}

func TestToolRouter_RunDispatchesToPluginOnUnknownLocal(t *testing.T) {
	local := &stubLocal{specs: []provider.ToolSpec{{Name: "read"}}}
	stub := &stubPluginClient{
		manifest: &pb.Manifest{
			OffersTools: []*pb.ToolSpec{{Name: "weather"}},
		},
		runResponse: &pb.RunToolResponse{Output: "sunny"},
	}
	r := NewToolRouter(local, regHostWith(t, stub))

	ctx := WithAgentID(t.Context(), "main")
	ctx = WithRunID(ctx, "run-xyz")
	out, err := r.Run(ctx, "weather", []byte(`{"city":"DEN"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "sunny" {
		t.Errorf("got %q, want sunny", out)
	}
	if len(stub.runCalls) != 1 {
		t.Fatalf("plugin RunTool calls = %d, want 1", len(stub.runCalls))
	}
	call := stub.runCalls[0]
	if call.GetToolName() != "weather" {
		t.Errorf("tool_name = %q", call.GetToolName())
	}
	if call.GetArgumentsJson() != `{"city":"DEN"}` {
		t.Errorf("arguments_json = %q", call.GetArgumentsJson())
	}
	if call.GetAgentId() != "main" || call.GetRunId() != "run-xyz" {
		t.Errorf("agent/run not propagated: agent=%q run=%q", call.GetAgentId(), call.GetRunId())
	}
}

func TestToolRouter_RunUnknownPropagatesUnknownToolError(t *testing.T) {
	local := &stubLocal{specs: nil}
	r := NewToolRouter(local, regHostWith(t, &stubPluginClient{manifest: &pb.Manifest{}}))
	_, err := r.Run(t.Context(), "missing", []byte(`{}`))
	if !errors.Is(err, tools.ErrUnknownTool) {
		t.Errorf("expected ErrUnknownTool, got %v", err)
	}
}

func TestToolRouter_RunPluginIsErrorSurfaceAsError(t *testing.T) {
	local := &stubLocal{}
	stub := &stubPluginClient{
		manifest: &pb.Manifest{
			OffersTools: []*pb.ToolSpec{{Name: "fragile"}},
		},
		runResponse: &pb.RunToolResponse{Output: "rate limited", IsError: true},
	}
	r := NewToolRouter(local, regHostWith(t, stub))

	out, err := r.Run(t.Context(), "fragile", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for is_error=true response")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should include the plugin output: %v", err)
	}
	if out != "rate limited" {
		t.Errorf("output should still surface even on error: %q", out)
	}
}

func TestToolRouter_RunPluginTransportErrorWrapped(t *testing.T) {
	local := &stubLocal{}
	stub := &stubPluginClient{
		manifest: &pb.Manifest{
			OffersTools: []*pb.ToolSpec{{Name: "x"}},
		},
		runErr: errors.New("connection reset"),
	}
	r := NewToolRouter(local, regHostWith(t, stub))
	_, err := r.Run(t.Context(), "x", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("transport error not wrapped: %v", err)
	}
	if !strings.Contains(err.Error(), "testplug") {
		t.Errorf("error should name the plugin: %v", err)
	}
}

// --- context propagation --------------------------------------------

func TestAgentIDContext_RoundTrip(t *testing.T) {
	if got := AgentIDFromContext(t.Context()); got != "" {
		t.Errorf("empty ctx should yield empty string, got %q", got)
	}
	ctx := WithAgentID(t.Context(), "coding")
	if got := AgentIDFromContext(ctx); got != "coding" {
		t.Errorf("AgentIDFromContext = %q", got)
	}
	// Empty id passed to With should be a no-op (don't write a key).
	if got := AgentIDFromContext(WithAgentID(t.Context(), "")); got != "" {
		t.Errorf("empty id should not register: %q", got)
	}
}

func TestRunIDContext_RoundTrip(t *testing.T) {
	if got := RunIDFromContext(t.Context()); got != "" {
		t.Errorf("empty ctx should yield empty, got %q", got)
	}
	ctx := WithRunID(t.Context(), "run-1")
	if got := RunIDFromContext(ctx); got != "run-1" {
		t.Errorf("got %q", got)
	}
}
