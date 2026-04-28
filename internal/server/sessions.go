package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// SessionPref is the per-session settings record. Fields holds the raw
// patch values keyed by openclaw's UI field names (model, modelProvider,
// thinkingLevel, verboseLevel, fastMode, ...). Storing them as raw JSON
// lets the patch endpoint accept any shape the UI sends and lets
// downstream consumers (chat.send today, more later) decode just the
// fields they care about.
type SessionPref struct {
	Fields    map[string]json.RawMessage `json:"fields"`
	UpdatedAt int64                      `json:"updatedAt"`
}

// SessionStore is an in-memory sessionKey → SessionPref map. Mutex-guarded
// for chat.send goroutines reading model overrides while the UI patches
// concurrently.
type SessionStore struct {
	mu    sync.Mutex
	prefs map[string]SessionPref
}

// NewSessionStore returns an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{prefs: make(map[string]SessionPref)}
}

// Patch merges fields into the entry under key. A field whose value is
// JSON null clears the entry's prior value for that key.
func (s *SessionStore) Patch(key string, fields map[string]json.RawMessage) SessionPref {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prefs[key]
	if !ok || p.Fields == nil {
		p.Fields = make(map[string]json.RawMessage)
	}
	for k, v := range fields {
		// Treat explicit null as a delete so the picker can revert to the
		// agent default; any other value (including empty string) is a
		// real set.
		if isJSONNull(v) {
			delete(p.Fields, k)
			continue
		}
		p.Fields[k] = v
	}
	p.UpdatedAt = time.Now().UnixMilli()
	s.prefs[key] = p
	return p
}

// Get returns the entry under key, or zero value if absent.
func (s *SessionStore) Get(key string) (SessionPref, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prefs[key]
	return p, ok
}

// Model returns the per-session model override, or "" when none.
// chat.send calls this before resolving the agent's PrimaryModel.
func (s *SessionStore) Model(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prefs[key]
	if !ok || p.Fields == nil {
		return ""
	}
	raw, ok := p.Fields["model"]
	if !ok {
		return ""
	}
	var m string
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m
}

// Snapshot returns a copy of all session entries. Used by sessions.list.
func (s *SessionStore) Snapshot() map[string]SessionPref {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]SessionPref, len(s.prefs))
	for k, v := range s.prefs {
		copyFields := make(map[string]json.RawMessage, len(v.Fields))
		for fk, fv := range v.Fields {
			copyFields[fk] = fv
		}
		out[k] = SessionPref{Fields: copyFields, UpdatedAt: v.UpdatedAt}
	}
	return out
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// SessionsHandler serves the openclaw sessions.* RPCs. ChatStore is
// optional — when set, sessions.list also includes sessions that have
// only chat history (no UI patches yet) so the picker can find them.
type SessionsHandler struct {
	store     *SessionStore
	chatStore *ChatStore
}

// NewSessionsHandler constructs a handler bound to store. chatStore may be
// nil; pass it to surface session keys that have history but no patches.
func NewSessionsHandler(store *SessionStore, chatStore *ChatStore) *SessionsHandler {
	return &SessionsHandler{store: store, chatStore: chatStore}
}

// Register wires sessions.patch, sessions.list, and sessions.subscribe.
func (h *SessionsHandler) Register(r *Registry) {
	r.Register("sessions.patch", h.handlePatch)
	r.Register("sessions.list", h.handleList)
	r.Register("sessions.subscribe", h.handleSubscribe)
}

// --- sessions.patch -------------------------------------------------------

// handlePatch accepts {key, ...patchFields} where patchFields are passed
// through to SessionStore.Patch. The response shape mirrors openclaw's
// SessionsPatchResultBase: {ok, path, key, entry}.
func (h *SessionsHandler) handlePatch(ctx context.Context, hc HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var raw map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "sessions.patch: " + err.Error()}
		}
	}
	keyRaw, ok := raw["key"]
	if !ok {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "sessions.patch: key is required"}
	}
	var key string
	if err := json.Unmarshal(keyRaw, &key); err != nil || key == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "sessions.patch: key must be a non-empty string"}
	}
	delete(raw, "key")

	pref := h.store.Patch(key, raw)
	return map[string]any{
		"ok":    true,
		"path":  "(in-memory)",
		"key":   key,
		"entry": prefAsEntry(key, pref),
	}, nil
}

// prefAsEntry renders a SessionPref into the row shape sessions.list
// returns. Patch fields are flattened to the top level so the UI's
// SessionRow picks them up directly.
func prefAsEntry(key string, p SessionPref) map[string]any {
	row := map[string]any{
		"key":       key,
		"kind":      "direct",
		"updatedAt": p.UpdatedAt,
	}
	for k, raw := range p.Fields {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			row[k] = v
		}
	}
	return row
}

// --- sessions.list --------------------------------------------------------

// handleList returns the openclaw SessionsListResult shape:
// {ts, path, count, defaults, sessions[]}. Defaults are minimal placeholders
// today; sessions include any session with prefs OR chat history.
func (h *SessionsHandler) handleList(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	now := time.Now().UnixMilli()
	rows := []map[string]any{}
	seen := map[string]bool{}

	for key, pref := range h.store.Snapshot() {
		rows = append(rows, prefAsEntry(key, pref))
		seen[key] = true
	}
	if h.chatStore != nil {
		for _, key := range h.chatStore.Keys() {
			if seen[key] {
				continue
			}
			rows = append(rows, map[string]any{
				"key":       key,
				"kind":      "direct",
				"updatedAt": now,
			})
		}
	}
	return map[string]any{
		"ts":    now,
		"path":  "(in-memory)",
		"count": len(rows),
		"defaults": map[string]any{
			"modelProvider": nil,
			"model":         nil,
			"contextTokens": nil,
		},
		"sessions": rows,
	}, nil
}

// --- sessions.subscribe ---------------------------------------------------

// handleSubscribe acks the UI's subscription. We don't yet push live
// session-update events; that lands when presence/active-run events do.
// The UI tolerates the no-op response and just relies on its own polling
// + sessions.list refreshes.
func (h *SessionsHandler) handleSubscribe(ctx context.Context, hc HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	return map[string]any{"ok": true}, nil
}
