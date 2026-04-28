package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// SessionRunner is the chat-side surface a ChannelDispatcher needs to
// turn an inbound channel message into an agent run. *server.ChatHandler
// satisfies this; declared here so internal/plugin doesn't have to import
// internal/server.
type SessionRunner interface {
	// RunForSession executes one agent turn whose history is keyed by
	// sessionKey. Subsequent calls with the same key continue the same
	// conversation. Returns the accumulated assistant text.
	RunForSession(ctx context.Context, sessionKey, agentID, message string) (string, error)
}

// ChannelBinding maps a channel offered by a plugin onto an agent and
// per-channel config blob. Built from the merged config tree's
// channels.<name> sub-tree.
type ChannelBinding struct {
	// ChannelName matches Manifest.OffersChannels (e.g. "telegram").
	ChannelName string
	// AgentID is the talon agent that handles inbound messages.
	AgentID string
	// ConfigJSON is the verbatim channels.<name> JSON; the plugin parses
	// it per its own schema (bot tokens, polling intervals, etc.).
	ConfigJSON []byte
}

// ChannelDispatcher pumps one (plugin, channel) pair: opens the
// plugin's StartChannel server-stream, dispatches each inbound message
// to SessionRunner.RunForSession with a deterministic per-(channel,
// sender) sessionKey, then sends the assistant's reply back via
// SendChannelMessage.
//
// Lifecycle: NewChannelDispatcher → Start → (cancel ctx or Stop). Run
// returns once the inbound stream EOFs, errors, or ctx is done.
type ChannelDispatcher struct {
	inst    *Instance
	binding ChannelBinding
	runner  SessionRunner

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	handlers sync.WaitGroup
}

// NewChannelDispatcher builds a dispatcher. inst.Client must be the
// plugin that offers binding.ChannelName; callers typically resolve via
// host.ChannelByName.
func NewChannelDispatcher(inst *Instance, binding ChannelBinding, runner SessionRunner) (*ChannelDispatcher, error) {
	if inst == nil || inst.Client == nil {
		return nil, errors.New("channel dispatcher: nil plugin instance")
	}
	if runner == nil {
		return nil, errors.New("channel dispatcher: nil session runner")
	}
	if strings.TrimSpace(binding.ChannelName) == "" {
		return nil, errors.New("channel dispatcher: empty channel name")
	}
	if strings.TrimSpace(binding.AgentID) == "" {
		return nil, errors.New("channel dispatcher: empty agent id")
	}
	return &ChannelDispatcher{
		inst:    inst,
		binding: binding,
		runner:  runner,
	}, nil
}

// Start launches the dispatch goroutine. Returns immediately; the
// goroutine runs until ctx is cancelled, the inbound stream ends, or
// Stop is called. Calling Start twice is a no-op.
func (d *ChannelDispatcher) Start(parentCtx context.Context) {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	d.cancel = cancel
	d.done = make(chan struct{})
	d.running = true
	d.mu.Unlock()

	go func() {
		defer close(d.done)
		if err := d.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[plugin/%s] channel %q stopped: %v",
				d.inst.Name, d.binding.ChannelName, err)
		}
		// Wait for in-flight per-message handlers to finish before
		// declaring done — Stop() needs every reply to be observable.
		d.handlers.Wait()
	}()
}

// Wait blocks until the dispatcher's run loop exits naturally (inbound
// EOF / stream error / ctx cancel) and all in-flight handlers have
// finished. Returns immediately if Start was never called. Useful for
// tests that want to observe natural drain without forcing cancel via
// Stop.
func (d *ChannelDispatcher) Wait() {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Stop cancels the dispatcher's context and waits for the goroutine to
// drain. Safe to call after Start or before; idempotent.
func (d *ChannelDispatcher) Stop() {
	d.mu.Lock()
	cancel := d.cancel
	done := d.done
	d.running = false
	d.cancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// run is the core pump loop. Splits out from Start so it's
// straight-line testable.
func (d *ChannelDispatcher) run(ctx context.Context) error {
	stream, err := d.inst.Client.StartChannel(ctx, &pb.StartChannelRequest{
		ChannelName:   d.binding.ChannelName,
		ChannelConfig: d.binding.ConfigJSON,
	})
	if err != nil {
		return fmt.Errorf("StartChannel: %w", err)
	}

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) || err == io.EOF {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Recv: %w", err)
		}
		// Each inbound message gets handled in its own goroutine so a
		// slow agent run doesn't stall the channel's inbound queue. The
		// plugin is responsible for backpressure on its side (e.g.
		// Telegram long-polling will just buffer). Tracked in handlers
		// so Stop() can wait for them.
		d.handlers.Add(1)
		go func(m *pb.IncomingChannelMessage) {
			defer d.handlers.Done()
			d.handle(ctx, m)
		}(msg)
	}
}

// handle runs one inbound message: builds a sessionKey, calls the
// agent, and posts the reply back. Logs errors but does not propagate —
// one bad message must not kill the whole channel.
func (d *ChannelDispatcher) handle(ctx context.Context, msg *pb.IncomingChannelMessage) {
	if msg == nil {
		return
	}
	if strings.TrimSpace(msg.GetText()) == "" {
		return
	}
	sessionKey := buildChannelSessionKey(d.binding.ChannelName, msg)

	reply, err := d.runner.RunForSession(ctx, sessionKey, d.binding.AgentID, msg.GetText())
	if err != nil {
		log.Printf("[plugin/%s] channel %q agent %q failed: %v",
			d.inst.Name, d.binding.ChannelName, d.binding.AgentID, err)
		return
	}
	if strings.TrimSpace(reply) == "" {
		return
	}

	roomID := msg.GetRoomId()
	if roomID == "" {
		roomID = msg.GetSenderId()
	}
	_, err = d.inst.Client.SendChannelMessage(ctx, &pb.SendChannelMessageRequest{
		Channel: d.binding.ChannelName,
		RoomId:  roomID,
		Text:    reply,
	})
	if err != nil {
		log.Printf("[plugin/%s] channel %q SendChannelMessage failed: %v",
			d.inst.Name, d.binding.ChannelName, err)
	}
}

// buildChannelSessionKey constructs the per-conversation sessionKey for
// channel messages. Group-style rooms key by room (so all members share
// state); direct messages key by sender. Format intentionally matches
// the "<scope>:<id>" convention used for subagent sessions.
func buildChannelSessionKey(channel string, msg *pb.IncomingChannelMessage) string {
	if room := msg.GetRoomId(); room != "" {
		return fmt.Sprintf("channel:%s:room:%s", channel, room)
	}
	return fmt.Sprintf("channel:%s:user:%s", channel, msg.GetSenderId())
}
