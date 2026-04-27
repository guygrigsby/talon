package server

import (
	"context"
	"encoding/json"
)

type HandlerCtx struct {
	Session *Session
}

type Handler func(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError)

type Registry struct {
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	r := &Registry{handlers: make(map[string]Handler)}
	r.registerDefaults()
	return r
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
	r.Register("health", func(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
		return map[string]any{
			"ok":       true,
			"server":   "talon-gateway",
			"uptimeMs": hc.Session.server.uptimeMs(),
			"version":  serverVersion,
		}, nil
	})
}
