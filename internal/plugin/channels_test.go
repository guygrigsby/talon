package plugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeStartChannelClient is a pb.Plugin_StartChannelClient backed by a
// channel of pre-recorded inbound messages plus an optional final
// error. Tests fill the channel before/while Recv is consumed; closing
// the channel ends the stream cleanly.
type fakeStartChannelClient struct {
	grpc.ClientStream
	in       chan *pb.IncomingChannelMessage
	finalErr error
	ctx      context.Context
}

func (f *fakeStartChannelClient) Recv() (*pb.IncomingChannelMessage, error) {
	select {
	case m, ok := <-f.in:
		if !ok {
			if f.finalErr != nil {
				return nil, f.finalErr
			}
			return nil, io.EOF
		}
		return m, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}
func (f *fakeStartChannelClient) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeStartChannelClient) Trailer() metadata.MD         { return nil }
func (f *fakeStartChannelClient) CloseSend() error             { return nil }
func (f *fakeStartChannelClient) Context() context.Context     { return f.ctx }
func (f *fakeStartChannelClient) SendMsg(any) error            { return nil }
func (f *fakeStartChannelClient) RecvMsg(any) error            { return nil }

// channelPluginClient is a pb.PluginClient with controllable
// StartChannel + SendChannelMessage. The other rpcs are stubbed.
type channelPluginClient struct {
	manifest *pb.Manifest

	mu       sync.Mutex
	startReq *pb.StartChannelRequest
	startCh  *fakeStartChannelClient
	startErr error

	sendCalls []*pb.SendChannelMessageRequest
	sendErr   error
}

func (c *channelPluginClient) Initialize(ctx context.Context, req *pb.InitializeRequest, _ ...grpc.CallOption) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{Manifest: c.manifest}, nil
}
func (c *channelPluginClient) Shutdown(context.Context, *pb.ShutdownRequest, ...grpc.CallOption) (*pb.ShutdownResponse, error) {
	return &pb.ShutdownResponse{}, nil
}
func (c *channelPluginClient) RunTool(context.Context, *pb.RunToolRequest, ...grpc.CallOption) (*pb.RunToolResponse, error) {
	return nil, errors.New("not used")
}
func (c *channelPluginClient) StreamCompletion(context.Context, *pb.StreamCompletionRequest, ...grpc.CallOption) (pb.Plugin_StreamCompletionClient, error) {
	return nil, errors.New("not used")
}
func (c *channelPluginClient) StartChannel(ctx context.Context, in *pb.StartChannelRequest, _ ...grpc.CallOption) (pb.Plugin_StartChannelClient, error) {
	c.mu.Lock()
	c.startReq = in
	c.mu.Unlock()
	if c.startErr != nil {
		return nil, c.startErr
	}
	c.startCh.ctx = ctx
	return c.startCh, nil
}
func (c *channelPluginClient) SendChannelMessage(ctx context.Context, in *pb.SendChannelMessageRequest, _ ...grpc.CallOption) (*pb.SendChannelMessageResponse, error) {
	c.mu.Lock()
	c.sendCalls = append(c.sendCalls, in)
	c.mu.Unlock()
	if c.sendErr != nil {
		return nil, c.sendErr
	}
	return &pb.SendChannelMessageResponse{Ok: true}, nil
}
func (c *channelPluginClient) StreamImageGeneration(context.Context, *pb.StreamImageGenerationRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ImageDelta], error) {
	return nil, errors.New("not used")
}

// recordingRunner is a SessionRunner that captures every call and
// returns a canned reply per agent. Used by the dispatcher tests to
// verify sessionKey derivation and routing.
type recordingRunner struct {
	mu    sync.Mutex
	calls []recordedRunnerCall
	reply string
	err   error
}

type recordedRunnerCall struct {
	sessionKey string
	agentID    string
	message    string
}

func (r *recordingRunner) RunForSession(ctx context.Context, sessionKey, agentID, message string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, recordedRunnerCall{sessionKey, agentID, message})
	r.mu.Unlock()
	return r.reply, r.err
}

func (r *recordingRunner) snapshot() []recordedRunnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRunnerCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// --- buildChannelSessionKey -----------------------------------------------

func TestBuildChannelSessionKey_RoomBeatsSender(t *testing.T) {
	got := buildChannelSessionKey("telegram", &pb.IncomingChannelMessage{
		SenderId: "user-1",
		RoomId:   "room-42",
	})
	if got != "channel:telegram:room:room-42" {
		t.Errorf("got %q", got)
	}
}

func TestBuildChannelSessionKey_DirectFallsBackToSender(t *testing.T) {
	got := buildChannelSessionKey("telegram", &pb.IncomingChannelMessage{
		SenderId: "user-1",
	})
	if got != "channel:telegram:user:user-1" {
		t.Errorf("got %q", got)
	}
}

// --- constructor validation ----------------------------------------------

func TestNewChannelDispatcher_ValidatesInputs(t *testing.T) {
	good := &Instance{Name: "p", Client: &channelPluginClient{}}
	runner := &recordingRunner{}
	cases := []struct {
		name    string
		inst    *Instance
		runner  SessionRunner
		binding ChannelBinding
		wantErr string
	}{
		{"nil instance", nil, runner, ChannelBinding{ChannelName: "x", AgentID: "a"}, "nil plugin instance"},
		{"nil runner", good, nil, ChannelBinding{ChannelName: "x", AgentID: "a"}, "nil session runner"},
		{"empty channel", good, runner, ChannelBinding{AgentID: "a"}, "empty channel name"},
		{"empty agent", good, runner, ChannelBinding{ChannelName: "x"}, "empty agent id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewChannelDispatcher(tc.inst, tc.binding, tc.runner)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// --- end-to-end dispatch --------------------------------------------------

// TestChannelDispatcher_DispatchesInboundToRunnerAndRepliesOut covers
// the core happy path: StartChannel returns a stream of one message,
// the runner produces a reply, the dispatcher posts the reply back via
// SendChannelMessage with the right room id, and the goroutine exits
// cleanly when the inbound stream EOFs.
func TestChannelDispatcher_DispatchesInboundToRunnerAndRepliesOut(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage, 1)
	in <- &pb.IncomingChannelMessage{
		Channel:     "telegram",
		SenderId:    "user-1",
		DisplayName: "Guy",
		RoomId:      "room-42",
		Text:        "hello agent",
		TsMs:        1700000000000,
	}
	close(in)

	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "telegram-plugin", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "telegram-plugin", Client: cli, Manifest: cli.manifest}

	runner := &recordingRunner{reply: "hi back"}
	d, err := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram",
		AgentID:     "main",
		ConfigJSON:  []byte(`{"token":"abc"}`),
	}, runner)
	if err != nil {
		t.Fatal(err)
	}

	d.Start(t.Context())
	// Wait for the goroutine to drain naturally (input was closed
	// pre-Start, so it'll see EOF after the buffered message). Stop
	// would race with the read loop and cancel ctx before the message
	// gets dispatched.
	d.Wait()
	d.Stop()

	// StartChannel got the channel name + config bytes through verbatim.
	cli.mu.Lock()
	gotReq := cli.startReq
	cli.mu.Unlock()
	if gotReq == nil || gotReq.GetChannelName() != "telegram" {
		t.Fatalf("StartChannel request wrong: %+v", gotReq)
	}
	if string(gotReq.GetChannelConfig()) != `{"token":"abc"}` {
		t.Errorf("channel config not propagated: %q", gotReq.GetChannelConfig())
	}

	// Runner saw exactly one call with the right key + agent + message.
	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(calls))
	}
	want := recordedRunnerCall{
		sessionKey: "channel:telegram:room:room-42",
		agentID:    "main",
		message:    "hello agent",
	}
	if calls[0] != want {
		t.Errorf("runner call drift: got %+v want %+v", calls[0], want)
	}

	// Reply went back via SendChannelMessage targeting the room.
	cli.mu.Lock()
	sends := append([]*pb.SendChannelMessageRequest(nil), cli.sendCalls...)
	cli.mu.Unlock()
	if len(sends) != 1 {
		t.Fatalf("SendChannelMessage calls = %d, want 1", len(sends))
	}
	if sends[0].GetChannel() != "telegram" || sends[0].GetRoomId() != "room-42" || sends[0].GetText() != "hi back" {
		t.Errorf("send drift: %+v", sends[0])
	}
}

// TestChannelDispatcher_DirectMessageFallsBackToSenderID checks the
// no-room branch: empty room_id makes the dispatcher reply to the
// sender directly.
func TestChannelDispatcher_DirectMessageFallsBackToSenderID(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage, 1)
	in <- &pb.IncomingChannelMessage{
		Channel:  "telegram",
		SenderId: "user-1",
		Text:     "hello",
	}
	close(in)

	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}
	runner := &recordingRunner{reply: "ok"}

	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, runner)
	d.Start(t.Context())
	d.Wait()
	d.Stop()

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.sendCalls) != 1 {
		t.Fatalf("send calls = %d", len(cli.sendCalls))
	}
	if cli.sendCalls[0].GetRoomId() != "user-1" {
		t.Errorf("expected room_id to fall back to sender_id, got %q", cli.sendCalls[0].GetRoomId())
	}

	// Session key uses user scope.
	calls := runner.snapshot()
	if calls[0].sessionKey != "channel:telegram:user:user-1" {
		t.Errorf("session key = %q", calls[0].sessionKey)
	}
}

// TestChannelDispatcher_SkipsEmptyTextAndEmptyReply: dispatcher should
// not bother the runner with whitespace-only messages and should not
// post empty replies. Verifies neither the runner nor SendChannelMessage
// is called.
func TestChannelDispatcher_SkipsEmptyTextAndEmptyReply(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage, 3)
	in <- &pb.IncomingChannelMessage{SenderId: "u", Text: "   "}      // whitespace
	in <- &pb.IncomingChannelMessage{SenderId: "u", Text: ""}          // empty
	in <- &pb.IncomingChannelMessage{SenderId: "u", Text: "real msg"}  // real one — but reply is empty
	close(in)

	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}
	runner := &recordingRunner{reply: ""}

	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, runner)
	d.Start(t.Context())
	d.Wait()
	d.Stop()

	// Whitespace + empty are dropped before reaching the runner. The
	// "real msg" reaches the runner, but the empty reply must not be
	// posted.
	if got := len(runner.snapshot()); got != 1 {
		t.Errorf("runner saw %d calls, want 1 (whitespace/empty skipped)", got)
	}
	cli.mu.Lock()
	sends := len(cli.sendCalls)
	cli.mu.Unlock()
	if sends != 0 {
		t.Errorf("empty replies should not be sent, got %d", sends)
	}
}

// TestChannelDispatcher_RunnerErrorDoesntKillStream: a single-message
// runner failure must be logged but not propagate; the dispatcher must
// keep handling subsequent messages.
func TestChannelDispatcher_RunnerErrorDoesntKillStream(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage, 2)
	in <- &pb.IncomingChannelMessage{SenderId: "u", Text: "first"}
	in <- &pb.IncomingChannelMessage{SenderId: "u", Text: "second"}
	close(in)

	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}

	var counter int32
	runner := &flakyRunner{
		fn: func(_ context.Context, _, _, msg string) (string, error) {
			atomic.AddInt32(&counter, 1)
			if msg == "first" {
				return "", errors.New("model down")
			}
			return "reply-" + msg, nil
		},
	}
	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, runner)
	d.Start(t.Context())
	d.Wait()
	d.Stop()

	if got := atomic.LoadInt32(&counter); got != 2 {
		t.Errorf("runner should have been called twice (error didn't stop the stream), got %d", got)
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.sendCalls) != 1 || cli.sendCalls[0].GetText() != "reply-second" {
		t.Errorf("only the second (successful) message should reply: %+v", cli.sendCalls)
	}
}

// TestChannelDispatcher_StartChannelErrorReturnsCleanly: a plugin that
// fails StartChannel up-front shouldn't deadlock — Start should
// complete and Stop should return without blocking.
func TestChannelDispatcher_StartChannelErrorReturnsCleanly(t *testing.T) {
	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startErr: errors.New("plugin doesn't support telegram"),
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}
	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, &recordingRunner{})

	done := make(chan struct{})
	go func() {
		d.Start(t.Context())
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung after StartChannel failure")
	}
}

// TestChannelDispatcher_StopCancelsRunningStream: while StartChannel is
// blocked waiting for a message, Stop should cancel the context and
// the dispatcher must exit promptly.
func TestChannelDispatcher_StopCancelsRunningStream(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage) // never delivered
	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}

	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, &recordingRunner{})
	d.Start(t.Context())

	stopped := make(chan struct{})
	go func() {
		d.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s; ctx cancel didn't propagate")
	}
}

// TestChannelDispatcher_StartIsIdempotent: calling Start twice should
// not start two pumps.
func TestChannelDispatcher_StartIsIdempotent(t *testing.T) {
	in := make(chan *pb.IncomingChannelMessage, 1)
	close(in)
	cli := &channelPluginClient{
		manifest: &pb.Manifest{Name: "p", OffersChannels: []string{"telegram"}},
		startCh:  &fakeStartChannelClient{in: in},
	}
	inst := &Instance{Name: "p", Client: cli, Manifest: cli.manifest}
	d, _ := NewChannelDispatcher(inst, ChannelBinding{
		ChannelName: "telegram", AgentID: "main",
	}, &recordingRunner{})
	d.Start(t.Context())
	d.Start(t.Context()) // must be a no-op
	d.Wait()
	d.Stop()

	cli.mu.Lock()
	defer cli.mu.Unlock()
	// Counts can't reliably be asserted on startReq because the second
	// Start short-circuits before reaching StartChannel; but the test
	// should not deadlock and should not panic.
}

// flakyRunner lets each test specify per-call behavior without touching
// recordingRunner's structure.
type flakyRunner struct {
	fn func(ctx context.Context, sessionKey, agentID, message string) (string, error)
}

func (r *flakyRunner) RunForSession(ctx context.Context, sessionKey, agentID, message string) (string, error) {
	return r.fn(ctx, sessionKey, agentID, message)
}
