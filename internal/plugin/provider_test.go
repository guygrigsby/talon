package plugin

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeStreamCompletionClient is a pb.Plugin_StreamCompletionClient backed
// by a recorded slice of deltas (or an error). Each Recv() pops the next
// item; once empty it returns io.EOF.
type fakeStreamCompletionClient struct {
	grpc.ClientStream
	deltas    []*pb.Delta
	idx       int
	finalErr  error // returned in place of EOF when set
}

func (f *fakeStreamCompletionClient) Recv() (*pb.Delta, error) {
	if f.idx >= len(f.deltas) {
		if f.finalErr != nil {
			return nil, f.finalErr
		}
		return nil, io.EOF
	}
	d := f.deltas[f.idx]
	f.idx++
	return d, nil
}
func (f *fakeStreamCompletionClient) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeStreamCompletionClient) Trailer() metadata.MD          { return nil }
func (f *fakeStreamCompletionClient) CloseSend() error              { return nil }
func (f *fakeStreamCompletionClient) Context() context.Context      { return context.Background() }
func (f *fakeStreamCompletionClient) SendMsg(any) error             { return nil }
func (f *fakeStreamCompletionClient) RecvMsg(any) error             { return nil }

// recordingPluginClient wraps stubPluginClient with an overridden
// StreamCompletion that returns a canned fakeStreamCompletionClient.
type recordingPluginClient struct {
	*stubPluginClient
	streamDeltas []*pb.Delta
	streamErr    error
	streamCalls  []*pb.StreamCompletionRequest
}

func (c *recordingPluginClient) StreamCompletion(ctx context.Context, in *pb.StreamCompletionRequest, _ ...grpc.CallOption) (pb.Plugin_StreamCompletionClient, error) {
	c.streamCalls = append(c.streamCalls, in)
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	return &fakeStreamCompletionClient{deltas: c.streamDeltas}, nil
}

// --- translateDelta -------------------------------------------------

func TestTranslateDelta_AllVariants(t *testing.T) {
	cases := []struct {
		name string
		in   *pb.Delta
		kind provider.DeltaKind
	}{
		{"text", &pb.Delta{Kind: &pb.Delta_Text{Text: "hi"}}, provider.DeltaText},
		{"reasoning", &pb.Delta{Kind: &pb.Delta_Reasoning{Reasoning: "thinking"}}, provider.DeltaReasoning},
		{"usage", &pb.Delta{Kind: &pb.Delta_Usage{Usage: &pb.Usage{InputTokens: 10}}}, provider.DeltaUsage},
		{"toolcall", &pb.Delta{Kind: &pb.Delta_ToolCall{ToolCall: &pb.ToolCall{Id: "c", Name: "n", ArgumentsJson: `{}`}}}, provider.DeltaToolCall},
		{"error", &pb.Delta{Kind: &pb.Delta_Error{Error: "rate limit"}}, provider.DeltaError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := translateDelta(tc.in)
			if !ok {
				t.Fatalf("translate failed for %s", tc.name)
			}
			if d.Kind != tc.kind {
				t.Errorf("kind = %v, want %v", d.Kind, tc.kind)
			}
		})
	}
}

func TestTranslateDelta_NilOrEmptyReturnsFalse(t *testing.T) {
	if _, ok := translateDelta(nil); ok {
		t.Errorf("nil should yield ok=false")
	}
	if _, ok := translateDelta(&pb.Delta{}); ok {
		t.Errorf("empty oneof should yield ok=false")
	}
}

func TestTranslateDelta_TextContent(t *testing.T) {
	d, _ := translateDelta(&pb.Delta{Kind: &pb.Delta_Text{Text: "hello"}})
	if d.Text != "hello" {
		t.Errorf("text = %q", d.Text)
	}
}

func TestTranslateDelta_ToolCallFields(t *testing.T) {
	d, _ := translateDelta(&pb.Delta{Kind: &pb.Delta_ToolCall{ToolCall: &pb.ToolCall{
		Id: "call_a", Name: "bash", ArgumentsJson: `{"command":"ls"}`,
	}}})
	if d.ToolCall == nil {
		t.Fatal("ToolCall is nil")
	}
	if d.ToolCall.ID != "call_a" || d.ToolCall.Name != "bash" || d.ToolCall.ArgumentsJSON != `{"command":"ls"}` {
		t.Errorf("tool call drift: %+v", d.ToolCall)
	}
}

func TestTranslateDelta_ErrorMessage(t *testing.T) {
	d, _ := translateDelta(&pb.Delta{Kind: &pb.Delta_Error{Error: "downstream 503"}})
	if d.Err == nil || !strings.Contains(d.Err.Error(), "503") {
		t.Errorf("error not surfaced: %v", d.Err)
	}
}

// --- PluginProvider --------------------------------------------------

func TestPluginProvider_StreamHappyPath(t *testing.T) {
	rec := &recordingPluginClient{
		stubPluginClient: &stubPluginClient{manifest: &pb.Manifest{}},
		streamDeltas: []*pb.Delta{
			{Kind: &pb.Delta_Text{Text: "Hello"}},
			{Kind: &pb.Delta_Text{Text: ", world"}},
			{Kind: &pb.Delta_Usage{Usage: &pb.Usage{InputTokens: 4, OutputTokens: 11}}},
		},
	}
	p := NewPluginProvider("weather-llm", rec)
	if p.Name() != "weather-llm" {
		t.Errorf("Name = %q", p.Name())
	}

	ch, err := p.Stream(t.Context(), provider.Request{
		Model:    "weather-llm/quick",
		System:   "You're brief.",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "yo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage *provider.Usage
	for d := range ch {
		switch d.Kind {
		case provider.DeltaText:
			text.WriteString(d.Text)
		case provider.DeltaUsage:
			usage = d.Usage
		}
	}
	if text.String() != "Hello, world" {
		t.Errorf("assembled text = %q", text.String())
	}
	if usage == nil || usage.InputTokens != 4 || usage.OutputTokens != 11 {
		t.Errorf("usage drift: %+v", usage)
	}

	// Verify request translation.
	if len(rec.streamCalls) != 1 {
		t.Fatalf("StreamCompletion calls = %d", len(rec.streamCalls))
	}
	got := rec.streamCalls[0]
	if got.GetModel() != "quick" {
		t.Errorf("model passed without prefix expected: got %q", got.GetModel())
	}
	if got.GetSystem() != "You're brief." {
		t.Errorf("system not propagated: %q", got.GetSystem())
	}
	if len(got.GetMessages()) != 1 || got.GetMessages()[0].GetRole() != pb.Role_ROLE_USER {
		t.Errorf("messages translation wrong: %+v", got.GetMessages())
	}
}

func TestPluginProvider_StreamRejectsWrongProviderSegment(t *testing.T) {
	p := NewPluginProvider("weather-llm", &stubPluginClient{manifest: &pb.Manifest{}})
	_, err := p.Stream(t.Context(), provider.Request{Model: "openai/gpt-4o"})
	if err == nil || !strings.Contains(err.Error(), "does not target") {
		t.Errorf("expected provider mismatch, got %v", err)
	}
}

func TestPluginProvider_StreamSetupErrorPropagates(t *testing.T) {
	rec := &recordingPluginClient{
		stubPluginClient: &stubPluginClient{manifest: &pb.Manifest{}},
		streamErr:        errors.New("dial closed"),
	}
	p := NewPluginProvider("x", rec)
	_, err := p.Stream(t.Context(), provider.Request{Model: "x/m"})
	if err == nil || !strings.Contains(err.Error(), "dial closed") {
		t.Errorf("setup error not propagated: %v", err)
	}
}

func TestPluginProvider_StreamMidFlightErrorEmitsErrorDelta(t *testing.T) {
	// Stream emits one text delta, then a transport-level error
	// instead of EOF. Provider should surface a DeltaError that wraps
	// the underlying gRPC error and close the channel.
	fake := &fakeStreamCompletionClient{
		deltas:   []*pb.Delta{{Kind: &pb.Delta_Text{Text: "partial"}}},
		finalErr: errors.New("connection reset"),
	}
	p := NewPluginProvider("x", &fakeReturnerClient{fake: fake, manifest: &pb.Manifest{}})

	ch, err := p.Stream(t.Context(), provider.Request{Model: "x/m"})
	if err != nil {
		t.Fatal(err)
	}
	var got []provider.Delta
	for d := range ch {
		got = append(got, d)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 deltas (partial + error), got %d", len(got))
	}
	if got[0].Kind != provider.DeltaText {
		t.Errorf("first delta should be text")
	}
	if got[1].Kind != provider.DeltaError || !strings.Contains(got[1].Err.Error(), "connection reset") {
		t.Errorf("second delta should wrap the transport error: %+v", got[1])
	}
}

// fakeReturnerClient is a minimal pb.PluginClient whose StreamCompletion
// returns a pre-constructed fake stream — used by the mid-flight-error
// test above where building a fresh shim each time would be noisier.
type fakeReturnerClient struct {
	manifest *pb.Manifest
	fake     pb.Plugin_StreamCompletionClient
}

func (c *fakeReturnerClient) Initialize(ctx context.Context, req *pb.InitializeRequest, _ ...grpc.CallOption) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{Manifest: c.manifest}, nil
}
func (c *fakeReturnerClient) Shutdown(ctx context.Context, req *pb.ShutdownRequest, _ ...grpc.CallOption) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}
func (c *fakeReturnerClient) RunTool(context.Context, *pb.RunToolRequest, ...grpc.CallOption) (*pb.RunToolResponse, error) {
	return nil, errors.New("not used")
}
func (c *fakeReturnerClient) StreamCompletion(context.Context, *pb.StreamCompletionRequest, ...grpc.CallOption) (pb.Plugin_StreamCompletionClient, error) {
	return c.fake, nil
}
func (c *fakeReturnerClient) StartChannel(context.Context, *pb.StartChannelRequest, ...grpc.CallOption) (pb.Plugin_StartChannelClient, error) {
	return nil, errors.New("not used")
}
func (c *fakeReturnerClient) SendChannelMessage(context.Context, *pb.SendChannelMessageRequest, ...grpc.CallOption) (*pb.SendChannelMessageResponse, error) {
	return nil, errors.New("not used")
}

// --- ProviderByName / ChannelByName --------------------------------

func TestHost_ProviderByName(t *testing.T) {
	h := NewHost("")
	stub := &stubPluginClient{manifest: &pb.Manifest{
		Name:            "weather-plugin",
		OffersProviders: []*pb.ProviderSpec{{Name: "weather-llm"}, {Name: "weather-llm-2"}},
	}}
	h.byName["weather-plugin"] = &Instance{
		Name:     "weather-plugin",
		Cookie:   "c",
		Manifest: stub.manifest,
		Client:   stub,
	}
	if got := h.ProviderByName("weather-llm"); got == nil || got.Name != "weather-plugin" {
		t.Errorf("ProviderByName(weather-llm): %+v", got)
	}
	if got := h.ProviderByName("weather-llm-2"); got == nil {
		t.Errorf("second offer not found")
	}
	if got := h.ProviderByName("unknown"); got != nil {
		t.Errorf("unknown provider should return nil")
	}
}

func TestHost_ChannelByName(t *testing.T) {
	h := NewHost("")
	stub := &stubPluginClient{manifest: &pb.Manifest{
		Name:           "telegram-plugin",
		OffersChannels: []string{"telegram"},
	}}
	h.byName["telegram-plugin"] = &Instance{
		Name:     "telegram-plugin",
		Cookie:   "c",
		Manifest: stub.manifest,
		Client:   stub,
	}
	if got := h.ChannelByName("telegram"); got == nil {
		t.Errorf("channel lookup failed")
	}
	if got := h.ChannelByName("nope"); got != nil {
		t.Errorf("missing channel should return nil")
	}
}
