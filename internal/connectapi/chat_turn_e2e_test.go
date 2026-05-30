package connectapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	talonv1connect "github.com/guygrigsby/talon/internal/api/v1/talon/v1/talonv1connect"
	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/chatdriver/modeltest"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// Full-stack chat turn, deterministic: a Connect client drives ChatService.Send
// + Subscribe against an in-process gateway whose chatdriver runs a scripted
// model (no network). Exercises the real path —
// chat.send -> ChatHandler -> chatdriver -> jess -> agentcore loop -> event
// stream -> SinkRegistry -> Connect Subscribe — and asserts thinking/delta/final
// arrive and history persists. (ADR 0016 Layer 1.)

type stubResolver struct{ model string }

func (s stubResolver) PrimaryModel(string) (provider.ModelID, error) {
	return provider.ModelID(s.model), nil
}

// stubFactory satisfies ProviderFactory but is never invoked: with a runner
// wired, handleSend routes every send through the chat driver.
type stubFactory struct{}

func (stubFactory) For(string, string) (provider.Provider, error) {
	return nil, fmt.Errorf("stubFactory.For should not be called on the runner path")
}

func writeChatFixture(t *testing.T) talonpath.Paths {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	runtimeJSON := `{"agents":{"defaults":{"model":{"primary":"fake/test-model"}},"list":[{"id":"main"}]}}`
	cfg, err := talonconfig.FromRuntimeJSON([]byte(runtimeJSON))
	if err != nil {
		t.Fatalf("FromRuntimeJSON: %v", err)
	}
	if err := os.WriteFile(cfgPath, talonconfig.MarshalTOML(cfg, talonconfig.MarshalOptions{}), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return talonpath.Paths{Talon: talonpath.Layer{Dir: dir, Config: cfgPath}}
}

func newChatGateway(t *testing.T, scripted *modeltest.Model) (*httptest.Server, talonv1connect.ChatServiceClient) {
	t.Helper()
	paths := writeChatFixture(t)
	store := server.NewChatStore()
	sinks := server.NewSinkRegistry()
	runner := chatdriver.NewChatRunner(paths, nil, nil, chatdriver.WithModelOverride(scripted))
	h := server.NewChatHandler(stubResolver{model: "fake/test-model"}, stubFactory{}, store).
		WithChatRunner(runner).
		WithSinks(sinks).
		WithPaths(paths)
	reg := server.NewRegistry()
	h.Register(reg)

	chat := &ChatService{Reg: reg, Sinks: sinks}
	mux := http.NewServeMux()
	mux.Handle(talonv1connect.NewChatServiceHandler(chat))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, talonv1connect.NewChatServiceClient(ts.Client(), ts.URL)
}

func TestChatTurn_EndToEnd_ScriptedModelStreams(t *testing.T) {
	const sessionKey = "agent:main:e2e"

	scripted := modeltest.New(modeltest.Turn{
		Reasoning:  []string{"let me think"},
		Text:       []string{"Hello ", "there"},
		StopReason: "stop",
	})
	ts, client := newChatGateway(t, scripted)
	_ = ts

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Subscribe first so the sink is registered before the turn runs.
	stream, err := client.Subscribe(ctx, connect.NewRequest(&talonv1.ChatSubscribeRequest{SessionKey: sessionKey}))
	if err != nil {
		t.Fatalf("Subscribe open: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() { // ready frame
		t.Fatalf("no ready frame: %v", stream.Err())
	}

	// Fire the turn.
	resp, err := client.Send(ctx, connect.NewRequest(&talonv1.ChatSendRequest{
		SessionKey: sessionKey, Message: "hi there", IdempotencyKey: "run-1",
	}))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Msg.GetRunId() != "run-1" {
		t.Errorf("runId = %q, want run-1", resp.Msg.GetRunId())
	}

	// Collect events until the final lands (or ctx times out).
	var thinking, lastDelta, finalText string
	var sawFinal bool
	for !sawFinal {
		if !stream.Receive() {
			t.Fatalf("stream closed before final: %v", stream.Err())
		}
		ev := stream.Msg()
		switch {
		case ev.GetThinking() != nil:
			thinking = ev.GetThinking().GetCumulative()
		case ev.GetDelta() != nil:
			lastDelta = ev.GetDelta().GetCumulative()
		case ev.GetError() != nil:
			t.Fatalf("unexpected error event: %s", ev.GetError().GetMessage())
		case ev.GetFinal() != nil:
			finalText = ev.GetFinal().GetText()
			sawFinal = true
		}
	}

	if !strings.Contains(thinking, "let me think") {
		t.Errorf("thinking cumulative = %q, want it to contain reasoning", thinking)
	}
	if lastDelta != "Hello there" {
		t.Errorf("last delta cumulative = %q, want %q", lastDelta, "Hello there")
	}
	if finalText != "Hello there" {
		t.Errorf("final text = %q, want %q", finalText, "Hello there")
	}

	// History persists the user turn and the assistant reply.
	hist, err := client.History(ctx, connect.NewRequest(&talonv1.ChatHistoryRequest{SessionKey: sessionKey}))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var sawUser, sawAssistant bool
	for _, row := range hist.Msg.GetMessages() {
		if u := row.GetUser(); u != nil && u.GetText() == "hi there" {
			sawUser = true
		}
		if a := row.GetAssistant(); a != nil && strings.Contains(a.GetText(), "Hello there") {
			sawAssistant = true
		}
	}
	if !sawUser {
		t.Error("history missing the user message")
	}
	if !sawAssistant {
		t.Error("history missing the assistant reply")
	}
}
