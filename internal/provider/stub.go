package provider

import (
	"context"
	"sync"
)

// Stub is a Provider that emits a pre-recorded Delta sequence on each Stream
// call, regardless of the request. Used for testing the gateway's chat-event
// fanout without depending on a live LLM. The same Stub may be reused across
// multiple Stream calls; each call produces a fresh channel.
type Stub struct {
	name   string
	script []Delta

	mu    sync.Mutex
	calls []Request // recorded calls, in order
}

// NewStub returns a Stub that identifies as name and replays script on each
// Stream invocation. The Stub takes ownership of script in the sense that
// callers should not mutate it after construction; it is shared across Stream
// invocations.
func NewStub(name string, script []Delta) *Stub {
	return &Stub{name: name, script: script}
}

func (s *Stub) Name() string { return s.name }

// Calls returns a copy of the requests Stream has been invoked with, in call
// order. Useful for assertions in tests.
func (s *Stub) Calls() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *Stub) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()

	ch := make(chan Delta)
	go func() {
		defer close(ch)
		for _, d := range s.script {
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
			if d.Kind == DeltaError {
				return
			}
		}
	}()
	return ch, nil
}
