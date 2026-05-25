package connectapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/server"
)

// End-to-end: a Connect client opens Subscribe, the server-side
// SinkRegistry broadcasts events, the client must receive the
// typed ChatEvent for each one. Covers the real handler path —
// connect.NewServerStream wiring, oneof codegen, proto JSON wire
// format — without booting a full talon gateway.

func TestChatSubscribe_EndToEnd_ReceivesBroadcastedEvents(t *testing.T) {
	sinks := server.NewSinkRegistry()
	svc := &ChatService{Sinks: sinks}

	mux := http.NewServeMux()
	mux.Handle(talonv1connect.NewChatServiceHandler(svc))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Connect's native protocol streams over HTTP/1.1 chunked
	// transfer-encoding — works against httptest.NewServer without
	// HTTP/2 / TLS setup gymnastics.
	client := talonv1connect.NewChatServiceClient(ts.Client(), ts.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, connect.NewRequest(&talonv1.ChatSubscribeRequest{
		SessionKey: "agent:test:1",
	}))
	if err != nil {
		t.Fatalf("Subscribe open: %v", err)
	}

	// Drain the initial ready frame (bare ChatEvent with no
	// payload variant set; sent by the handler to flush headers).
	if !stream.Receive() {
		t.Fatalf("expected initial ready frame, stream closed early: err=%v", stream.Err())
	}
	if got := stream.Msg(); got.GetPayload() != nil {
		t.Errorf("ready frame should have no payload, got %T", got.GetPayload())
	}

	// Subscriber is registered before Send returns — the handler
	// does Subscribe → Send(ready) → block, so once Receive
	// returned the sink must be in the registry.
	if got := sinks.SubscriberCount("agent:test:1"); got != 1 {
		t.Errorf("SubscriberCount = %d, want 1", got)
	}

	// Broadcast a delta. The chat handler emit path uses exactly
	// this signature: (sessionKey, "chat", ChatEventPayload).
	sinks.Broadcast(context.Background(), "agent:test:1", "chat", server.ChatEventPayload{
		RunID: "r1", SessionKey: "agent:test:1", Seq: 1, State: "delta", DeltaText: "hi",
		Message: &server.ChatEventMessage{
			Content: []server.ChatEventContentPart{{Type: "text", Text: "hi"}},
		},
	})

	if !stream.Receive() {
		t.Fatalf("expected one event, stream closed early: err=%v", stream.Err())
	}
	got := stream.Msg()
	if got.GetRunId() != "r1" {
		t.Errorf("run_id = %q, want r1", got.GetRunId())
	}
	d := got.GetDelta()
	if d == nil {
		t.Fatalf("expected Delta variant, got %T", got.GetPayload())
	}
	if d.GetDeltaText() != "hi" || d.GetCumulative() != "hi" {
		t.Errorf("delta fields wrong: %+v", d)
	}

	// Second event: an agent.tool start. Verifies multi-event
	// streaming and that the agent translation path works through
	// the same stream.
	sinks.Broadcast(context.Background(), "agent:test:1", "agent", server.AgentEventPayload{
		Stream: "tool", SessionKey: "agent:test:1", RunID: "r1", Ts: 1700000000000,
		Data: map[string]any{
			"toolCallId": "call_a", "name": "glob", "phase": "start",
			"args": map[string]any{"pattern": "*.go"},
		},
	})
	if !stream.Receive() {
		t.Fatalf("expected tool start, stream closed early: err=%v", stream.Err())
	}
	if ts := stream.Msg().GetToolStart(); ts == nil || ts.GetName() != "glob" {
		t.Errorf("expected ToolStart with name=glob, got %+v", stream.Msg())
	}

	// Closing the stream from the client side must unsubscribe
	// the sink server-side so the registry doesn't leak.
	if err := stream.Close(); err != nil {
		t.Errorf("stream close: %v", err)
	}
	waitFor(t, "sink unsubscribed", func() bool {
		return sinks.SubscriberCount("agent:test:1") == 0
	})
}

// Subscribe with run_id filters out events whose RunID differs.
func TestChatSubscribe_EndToEnd_RunIDFiltering(t *testing.T) {
	sinks := server.NewSinkRegistry()
	svc := &ChatService{Sinks: sinks}

	mux := http.NewServeMux()
	mux.Handle(talonv1connect.NewChatServiceHandler(svc))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Connect's native protocol streams over HTTP/1.1 chunked
	// transfer-encoding — works against httptest.NewServer without
	// HTTP/2 / TLS setup gymnastics.
	client := talonv1connect.NewChatServiceClient(ts.Client(), ts.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, connect.NewRequest(&talonv1.ChatSubscribeRequest{
		SessionKey: "agent:test:1",
		RunId:      "wanted",
	}))
	if err != nil {
		t.Fatalf("Subscribe open: %v", err)
	}
	defer stream.Close()

	// Drain the ready frame.
	if !stream.Receive() {
		t.Fatalf("expected initial ready frame, stream closed early: err=%v", stream.Err())
	}

	// Off-run event must drop.
	sinks.Broadcast(context.Background(), "agent:test:1", "chat", server.ChatEventPayload{
		RunID: "other", State: "delta", DeltaText: "skip",
	})
	// Wanted-run event must arrive.
	sinks.Broadcast(context.Background(), "agent:test:1", "chat", server.ChatEventPayload{
		RunID: "wanted", State: "delta", DeltaText: "keep",
	})

	if !stream.Receive() {
		t.Fatalf("expected one event, stream closed early: err=%v", stream.Err())
	}
	got := stream.Msg()
	if got.GetRunId() != "wanted" {
		t.Errorf("filtered stream got run=%q, want wanted", got.GetRunId())
	}
}

func TestChatSubscribe_MissingSessionKey(t *testing.T) {
	svc := &ChatService{Sinks: server.NewSinkRegistry()}
	mux := http.NewServeMux()
	mux.Handle(talonv1connect.NewChatServiceHandler(svc))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Connect's native protocol streams over HTTP/1.1 chunked
	// transfer-encoding — works against httptest.NewServer without
	// HTTP/2 / TLS setup gymnastics.
	client := talonv1connect.NewChatServiceClient(ts.Client(), ts.URL)
	stream, err := client.Subscribe(context.Background(),
		connect.NewRequest(&talonv1.ChatSubscribeRequest{}))
	if err != nil {
		t.Fatalf("Subscribe open: %v", err)
	}
	defer stream.Close()

	// First Receive must surface the server-side validation error.
	if stream.Receive() {
		t.Fatalf("expected no events, got %+v", stream.Msg())
	}
	ce := new(connect.Error)
	if !errorsAs(stream.Err(), &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Errorf("err = %v, want CodeInvalidArgument", stream.Err())
	}
}

// waitFor polls f every 5ms for up to 1s. Used in place of a
// raw sleep so timing-dependent assertions stay tight in fast
// runs but don't flake on a loaded CI.
func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// errorsAs wraps errors.As so callers don't have to import errors
// for one-off type assertions in test bodies.
func errorsAs(err error, target any) bool {
	if err == nil {
		return false
	}
	type asTarget interface {
		As(any) bool
	}
	// stdlib path
	if e, ok := target.(**connect.Error); ok {
		var ce *connect.Error
		if connectErrorAs(err, &ce) {
			*e = ce
			return true
		}
	}
	return false
}

func connectErrorAs(err error, dst **connect.Error) bool {
	for err != nil {
		if ce, ok := err.(*connect.Error); ok {
			*dst = ce
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
