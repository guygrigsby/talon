package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// captureSink stores everything pushed to it for assertions.
// Concurrency-safe so the broadcast snapshot fan-out tests can
// run on the race detector without flake.
type captureSink struct {
	mu     sync.Mutex
	events []captureEvent
	failOn string // event name to return an error from (drops nothing else)
}

type captureEvent struct {
	event   string
	payload any
}

func (c *captureSink) PushEvent(_ context.Context, event string, payload any) error {
	c.mu.Lock()
	c.events = append(c.events, captureEvent{event: event, payload: payload})
	c.mu.Unlock()
	if c.failOn != "" && event == c.failOn {
		return errors.New("captureSink: failed by design")
	}
	return nil
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func TestSinkRegistry_BroadcastWithNoSubscribers(t *testing.T) {
	// No subscribers → no panic, no work. Common case on the WS
	// path before any Connect Subscribe lands; must be cheap.
	r := NewSinkRegistry()
	r.Broadcast(context.Background(), "agent:main:main", "chat", "payload")
}

func TestSinkRegistry_SubscribeAndBroadcast(t *testing.T) {
	r := NewSinkRegistry()
	sink := &captureSink{}
	unsub := r.Subscribe("agent:main:main", sink)
	defer unsub()

	r.Broadcast(context.Background(), "agent:main:main", "chat", "payload-1")
	r.Broadcast(context.Background(), "agent:main:main", "chat", "payload-2")

	if got := sink.count(); got != 2 {
		t.Fatalf("got %d events, want 2", got)
	}
	if sink.events[0].event != "chat" || sink.events[0].payload != "payload-1" {
		t.Errorf("event 0 wrong: %+v", sink.events[0])
	}
}

func TestSinkRegistry_BroadcastIsKeyScoped(t *testing.T) {
	// Subscribers on key A don't see events on key B.
	r := NewSinkRegistry()
	a := &captureSink{}
	b := &captureSink{}
	defer r.Subscribe("agent:a:1", a)()
	defer r.Subscribe("agent:b:1", b)()

	r.Broadcast(context.Background(), "agent:a:1", "chat", "for-a")

	if a.count() != 1 {
		t.Errorf("sink A: got %d events, want 1", a.count())
	}
	if b.count() != 0 {
		t.Errorf("sink B: got %d events, want 0", b.count())
	}
}

func TestSinkRegistry_MultipleSubscribersOnSameKey(t *testing.T) {
	// Connect Subscribe + a debug tap could both subscribe to the
	// same session-key. Each one must receive every event.
	r := NewSinkRegistry()
	s1, s2 := &captureSink{}, &captureSink{}
	defer r.Subscribe("k", s1)()
	defer r.Subscribe("k", s2)()

	r.Broadcast(context.Background(), "k", "agent", "fanout")

	if s1.count() != 1 || s2.count() != 1 {
		t.Errorf("each sink should get exactly one event: s1=%d s2=%d", s1.count(), s2.count())
	}
}

func TestSinkRegistry_UnsubscribeStopsDelivery(t *testing.T) {
	r := NewSinkRegistry()
	sink := &captureSink{}
	unsub := r.Subscribe("k", sink)

	r.Broadcast(context.Background(), "k", "chat", "1")
	unsub()
	r.Broadcast(context.Background(), "k", "chat", "2")

	if sink.count() != 1 {
		t.Errorf("got %d events, want 1 (second broadcast was after unsubscribe)", sink.count())
	}
	if r.SubscriberCount("k") != 0 {
		t.Errorf("SubscriberCount after unsub = %d, want 0", r.SubscriberCount("k"))
	}
}

// A failing sink must not stop delivery to its siblings. The chat
// run has no useful recovery path for a single failed push, so the
// registry logs and moves on.
func TestSinkRegistry_FailingSinkDoesNotBlockSiblings(t *testing.T) {
	r := NewSinkRegistry()
	bad := &captureSink{failOn: "chat"}
	good := &captureSink{}
	defer r.Subscribe("k", bad)()
	defer r.Subscribe("k", good)()

	r.Broadcast(context.Background(), "k", "chat", "fanout")

	if bad.count() != 1 {
		t.Errorf("failing sink should still see the push attempt; got %d", bad.count())
	}
	if good.count() != 1 {
		t.Errorf("sibling sink should still receive after the failure; got %d", good.count())
	}
}

// nil-safety: a nil receiver, nil sink, or empty key must not
// panic — the registry is wired pre-handshake on Server.New, and
// the chat handler can emit before any subscriber exists.
func TestSinkRegistry_NilSafety(t *testing.T) {
	var r *SinkRegistry
	r.Broadcast(context.Background(), "k", "chat", "x")
	if got := r.SubscriberCount("k"); got != 0 {
		t.Errorf("SubscriberCount on nil registry = %d, want 0", got)
	}
	unsub := r.Subscribe("k", &captureSink{})
	unsub() // must be safe even from nil

	r2 := NewSinkRegistry()
	if got := r2.Subscribe("", &captureSink{}); got == nil {
		t.Error("Subscribe with empty key should still return a callable unsub")
	}
	if got := r2.Subscribe("k", nil); got == nil {
		t.Error("Subscribe with nil sink should still return a callable unsub")
	}
	r2.Broadcast(context.Background(), "", "chat", "x") // empty key = no-op
}

// Concurrent broadcasts + subscribes: the snapshot pattern in
// Broadcast must allow long-running sinks not to block writers.
// Uses the race detector implicitly via the test runner flags.
func TestSinkRegistry_ConcurrentBroadcastAndSubscribe(t *testing.T) {
	r := NewSinkRegistry()
	var seen atomic.Int64
	mk := func() EventSink { return &countingSink{n: &seen} }

	defer r.Subscribe("k", mk())()

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			for j := range 50 {
				r.Broadcast(context.Background(), "k", "chat", j)
			}
		})
	}
	// Churning subscribers in parallel with broadcasts must not
	// race with the snapshot.
	for range 5 {
		wg.Go(func() {
			for range 20 {
				unsub := r.Subscribe("k", mk())
				unsub()
			}
		})
	}
	wg.Wait()
	if seen.Load() == 0 {
		t.Error("expected at least one broadcast to land on the persistent sink")
	}
}

type countingSink struct{ n *atomic.Int64 }

func (c *countingSink) PushEvent(_ context.Context, _ string, _ any) error {
	c.n.Add(1)
	return nil
}
