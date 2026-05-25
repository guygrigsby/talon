package server

import (
	"context"
	"log/slog"
	"sync"
)

// EventSink consumes server-pushed events scoped to one session
// key. Session implements it for the WS path; the Connect path's
// ChatService.Subscribe implements it on top of a ServerStream.
//
// The signature mirrors Session.PushEvent so a *Session satisfies
// the interface trivially. Payload is the same Go value the WS
// path JSON-marshals into the frame body; the Connect sink
// type-asserts on it to translate into the typed ChatEvent oneof.
type EventSink interface {
	PushEvent(ctx context.Context, event string, payload any) error
}

// SinkRegistry fans server-pushed events out to every sink
// subscribed to a session key. Additive next to the direct
// Session push the chat handler still performs: the WS path
// behaves exactly as before, the Connect path gets the same
// events through Subscribe.
//
// Concurrency: reads outnumber writes by orders of magnitude
// (one chat run emits dozens of events; subscriptions
// register / drop once per stream lifetime). RWMutex with a
// snapshot pattern under Broadcast so a slow sink can't block
// the read lock while it writes.
type SinkRegistry struct {
	mu    sync.RWMutex
	sinks map[string][]EventSink
}

func NewSinkRegistry() *SinkRegistry {
	return &SinkRegistry{sinks: make(map[string][]EventSink)}
}

// Subscribe registers sink for sessionKey events and returns an
// unsubscribe func the caller must run when the subscription
// ends (defer in a Subscribe RPC handler). Multiple sinks per
// sessionKey are fine; each gets a copy of every event.
func (r *SinkRegistry) Subscribe(sessionKey string, sink EventSink) (unsubscribe func()) {
	if r == nil || sessionKey == "" || sink == nil {
		return func() {}
	}
	r.mu.Lock()
	r.sinks[sessionKey] = append(r.sinks[sessionKey], sink)
	r.mu.Unlock()
	return func() { r.unsubscribe(sessionKey, sink) }
}

func (r *SinkRegistry) unsubscribe(sessionKey string, sink EventSink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.sinks[sessionKey]
	for i, s := range list {
		if s == sink {
			r.sinks[sessionKey] = append(list[:i], list[i+1:]...)
			if len(r.sinks[sessionKey]) == 0 {
				delete(r.sinks, sessionKey)
			}
			return
		}
	}
}

// Broadcast pushes one event to every sink subscribed to
// sessionKey. Failures are logged but don't propagate — one slow
// or dead sink can't block the others, and the chat run that
// triggered the emit has no useful recovery path. Snapshot under
// the read lock so write paths aren't blocked while sinks deliver.
func (r *SinkRegistry) Broadcast(ctx context.Context, sessionKey, event string, payload any) {
	if r == nil || sessionKey == "" {
		return
	}
	r.mu.RLock()
	list := r.sinks[sessionKey]
	if len(list) == 0 {
		r.mu.RUnlock()
		return
	}
	snapshot := make([]EventSink, len(list))
	copy(snapshot, list)
	r.mu.RUnlock()
	for _, sink := range snapshot {
		if err := sink.PushEvent(ctx, event, payload); err != nil {
			slog.Debug("event sink push failed",
				"sessionKey", sessionKey, "event", event, "err", err)
		}
	}
}

// SubscriberCount returns how many sinks are listening on
// sessionKey. Useful in tests and (eventually) for debug
// endpoints; intentionally not used on hot paths.
func (r *SinkRegistry) SubscriberCount(sessionKey string) int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sinks[sessionKey])
}
