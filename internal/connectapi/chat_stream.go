package connectapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// connectSink adapts a Connect ServerStream into a server.EventSink.
// One instance per active Subscribe call. PushEvent runs on the
// chat handler's emit goroutine; the Subscribe handler itself
// sends the initial ready frame from the request goroutine. Two
// senders on one ServerStream — and connect.ServerStream.Send is
// not safe for concurrent use — so writes are serialized through
// sendMu.
//
// Filter: if runID is non-empty the sink drops events that don't
// match. Matches the WS contract where state.chatRunId scopes a stream to
// one run.
type connectSink struct {
	stream *connect.ServerStream[talonv1.ChatEvent]
	runID  string

	sendMu sync.Mutex
	closed bool
}

// send serializes writes to the wrapped ServerStream. All Send
// call sites (the initial ready frame and PushEvent) must go
// through it. A send after close is a no-op: SinkRegistry's
// snapshot-then-iterate pattern can call PushEvent on a sink that
// was unsubscribed mid-broadcast, and after the handler returns,
// Connect's internal Close flushes the underlying response writer
// concurrently — without the closed-flag gate, those collide.
func (s *connectSink) send(ev *talonv1.ChatEvent) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return nil
	}
	return s.stream.Send(ev)
}

// close marks the sink rejected for future sends and waits for
// any in-flight one to finish under sendMu. Call from the
// Subscribe handler right before it returns so the Connect
// runtime's response cleanup doesn't race with a still-running
// Broadcast goroutine.
func (s *connectSink) close() {
	s.sendMu.Lock()
	s.closed = true
	s.sendMu.Unlock()
}

func (s *connectSink) PushEvent(_ context.Context, event string, payload any) error {
	ev := translateEvent(event, payload)
	if ev == nil {
		return nil
	}
	if s.runID != "" && ev.GetRunId() != "" && ev.GetRunId() != s.runID {
		return nil
	}
	if err := s.send(ev); err != nil {
		// Wrap so the SinkRegistry's debug log carries the
		// originating event name for triage.
		return fmt.Errorf("chat.Subscribe send (%s): %w", event, err)
	}
	return nil
}

// translateEvent maps the WS-style (event-name, payload) pair onto
// a typed talonv1.ChatEvent. Returns nil for shapes the Connect
// contract doesn't model — those silently drop on the Connect path
// (the WS path still delivers them). Defensive on payload type so a
// future emitter that uses a different shape doesn't panic the
// stream.
func translateEvent(event string, payload any) *talonv1.ChatEvent {
	switch event {
	case "chat":
		p, ok := payload.(server.ChatEventPayload)
		if !ok {
			return nil
		}
		return chatPayloadToEvent(p)
	case "agent":
		p, ok := payload.(server.AgentEventPayload)
		if !ok {
			return nil
		}
		return agentPayloadToEvent(p)
	default:
		return nil
	}
}

func chatPayloadToEvent(p server.ChatEventPayload) *talonv1.ChatEvent {
	ev := &talonv1.ChatEvent{
		TsMs:       time.Now().UnixMilli(),
		RunId:      p.RunID,
		SessionKey: p.SessionKey,
		Seq:        int32(p.Seq),
	}
	switch p.State {
	case "delta":
		ev.Payload = &talonv1.ChatEvent_Delta{Delta: &talonv1.ChatDelta{
			Cumulative: messageText(p.Message),
			DeltaText:  p.DeltaText,
			Replace:    p.Replace,
		}}
	case "thinking":
		ev.Payload = &talonv1.ChatEvent_Thinking{Thinking: &talonv1.ChatThinking{
			Cumulative: messageText(p.Message),
			DeltaText:  p.DeltaText,
		}}
	case "final":
		ev.Payload = &talonv1.ChatEvent_Final{Final: &talonv1.ChatFinal{
			Text:       messageText(p.Message),
			StopReason: p.StopReason,
		}}
	case "aborted":
		ev.Payload = &talonv1.ChatEvent_Aborted{Aborted: &talonv1.ChatAborted{
			Text: messageText(p.Message),
		}}
	case "error":
		ev.Payload = &talonv1.ChatEvent_Error{Error: &talonv1.ChatError{
			Kind:    p.ErrorKind,
			Message: p.ErrorMessage,
		}}
	default:
		// Unknown chat state — drop. WS clients still see it via
		// the direct push; Connect callers asked for typed events
		// and we don't have a variant for unrecognized states.
		return nil
	}
	return ev
}

func agentPayloadToEvent(p server.AgentEventPayload) *talonv1.ChatEvent {
	// The only agent stream the chat handler emits today is
	// stream="tool" with data.phase ∈ {start, result}. Anything
	// else falls through to a drop.
	if p.Stream != "tool" || p.Data == nil {
		return nil
	}
	phase, _ := p.Data["phase"].(string)
	toolCallID, _ := p.Data["toolCallId"].(string)
	name, _ := p.Data["name"].(string)
	ev := &talonv1.ChatEvent{
		TsMs:       p.Ts,
		RunId:      p.RunID,
		SessionKey: p.SessionKey,
	}
	switch phase {
	case "start":
		ev.Payload = &talonv1.ChatEvent_ToolStart{ToolStart: &talonv1.ToolStart{
			ToolCallId: toolCallID,
			Name:       name,
			ArgsJson:   stringifyArgs(p.Data["args"]),
		}}
	case "result":
		output, _ := p.Data["result"].(string)
		ev.Payload = &talonv1.ChatEvent_ToolResult{ToolResult: &talonv1.ToolResult{
			ToolCallId: toolCallID,
			Name:       name,
			Output:     output,
		}}
	default:
		return nil
	}
	return ev
}

// messageText pulls the cumulative assistant text out of a chat
// event's message structure. Returns "" if message or content is
// empty. The WS handler always puts the cumulative text in
// content[0].text on emit (see chat.go emitChat).
func messageText(m *server.ChatEventMessage) string {
	if m == nil {
		return ""
	}
	for _, c := range m.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// stringifyArgs renders the tool arguments back to a JSON string.
// The WS handler stashes a JSON-decoded `any` in data.args; the
// proto contract is a JSON string (so tools can declare arbitrary
// arg shapes without proto changes). Round-trip via Marshal.
// On error or absence, return "" rather than failing the event —
// clients can render the tool card without args if they have to.
func stringifyArgs(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		// Fallback path in buildToolStartPayload puts the raw
		// arguments string here when JSON-parse failed. Forward
		// as-is.
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Subscribe replaces the stage-1 Unimplemented stub. Subscribes a
// connectSink against the server's SinkRegistry for the requested
// session-key, then blocks until the client disconnects. Each
// event the chat handler emits to that key is translated and
// streamed to the client.
//
// The session-key is required (no all-session subscription —
// callers should subscribe per channel they care about; an
// authenticated user would also need scope checks for arbitrary
// keys, which we don't model yet).
//
// Lifetime: when the client closes the stream OR the request
// context is canceled (e.g. gateway shutdown), we unsubscribe and
// return nil. A handler that returns is what closes the HTTP
// response on the Connect side.
func (s *ChatService) Subscribe(ctx context.Context, req *connect.Request[talonv1.ChatSubscribeRequest], stream *connect.ServerStream[talonv1.ChatEvent]) error {
	if s.Sinks == nil {
		return connect.NewError(connect.CodeUnimplemented,
			errors.New("chat.Subscribe not wired (no sink registry)"))
	}
	sessionKey := req.Msg.GetSessionKey()
	if sessionKey == "" {
		return connect.NewError(connect.CodeInvalidArgument,
			errors.New("chat.Subscribe: session_key is required"))
	}
	sink := &connectSink{stream: stream, runID: req.Msg.GetRunId()}
	unsub := s.Sinks.Subscribe(sessionKey, sink)
	// LIFO: close runs last. unsub first removes the sink from the
	// registry so no fresh Broadcast snapshots see it; close then
	// waits under sendMu for any in-flight PushEvent to finish and
	// blocks all subsequent sends. Both must run before the handler
	// returns or Connect's internal Close will race a still-running
	// stream.Send.
	defer sink.close()
	defer unsub()
	// Flush response headers immediately. Connect's server-stream
	// over HTTP/1.1 doesn't send response headers until the handler
	// writes (or returns); without a kickoff frame, the client's
	// first Receive blocks waiting for headers while we block on
	// ctx.Done() — classic deadlock. A bare ChatEvent (no payload
	// variant) is the smallest legal frame; the FE filters on
	// payload variant and ignores it. Send failure here means the
	// client is already gone, so bail.
	if err := sink.send(&talonv1.ChatEvent{SessionKey: sessionKey}); err != nil {
		return nil
	}
	// Select on the request ctx AND the registry's drain channel.
	// The request ctx fires on normal client disconnect; the drain
	// fires on gateway shutdown (Ctrl-C). Without the second case,
	// a Ctrl-C would wait the http.Server.Shutdown timeout for
	// every open Subscribe to time out individually.
	select {
	case <-ctx.Done():
	case <-s.Sinks.Drain():
	}
	// ctx.Err is Canceled for normal client disconnect and
	// DeadlineExceeded for forced cutoff; both map cleanly to a
	// successful stream-close on the Connect side. Returning the
	// err would surface it as a "non-OK status" on the client,
	// which it isn't.
	return nil
}
