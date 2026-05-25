package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"
)

// HandlerCtx is the per-call context the registry passes to
// every Handler. Empty today (the WS-era Session field went away
// when the WS path was stripped); kept as a named struct so new
// per-call state can land without churning every handler signature.
type HandlerCtx struct{}

type Handler func(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError)

type Registry struct {
	handlers map[string]Handler
	// serverStart is set by Server.New once the gateway boots, so
	// the default `health` handler can report uptime without
	// reaching through a Session. Atomic-stored as Unix-nanos so
	// the read path stays lock-free.
	serverStart atomic.Int64
}

func NewRegistry() *Registry {
	r := &Registry{handlers: make(map[string]Handler)}
	r.serverStart.Store(time.Now().UnixNano())
	r.registerDefaults()
	return r
}

// SetServerStart anchors uptime reporting on the default `health`
// handler. Server.New calls it once, immediately after Registry
// is constructed; tests can call it directly to control the
// reference instant.
func (r *Registry) SetServerStart(t time.Time) {
	r.serverStart.Store(t.UnixNano())
}

func (r *Registry) Register(method string, h Handler) {
	r.handlers[method] = h
}

func (r *Registry) Methods() []string {
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	return out
}

func (r *Registry) Dispatch(ctx context.Context, hc HandlerCtx, method string, params json.RawMessage) (any, *FrameError) {
	h, ok := r.handlers[method]
	if !ok {
		return nil, &FrameError{Code: ErrCodeMethodNotFound, Message: "unknown method: " + method}
	}
	return h(ctx, hc, params)
}

func (r *Registry) registerDefaults() {
	r.Register("health", func(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
		uptimeMs := int64(0)
		if start := r.serverStart.Load(); start > 0 {
			uptimeMs = (time.Now().UnixNano() - start) / int64(time.Millisecond)
		}
		return map[string]any{
			"ok":       true,
			"server":   "talon-gateway",
			"uptimeMs": uptimeMs,
			"version":  serverVersion,
		}, nil
	})
}
