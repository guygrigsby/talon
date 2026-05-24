package server

import "net/http"

// Mux exposes the server's HTTP mux so callers (cmd/talon) can
// register additional handlers — Connect services, custom static
// paths, future protocols — without the server package needing
// to know about them.
//
// Lifecycle: safe to call after server.New returns. Routes added
// via Mux() take effect on the next request — there's no
// rebuild step. Callers must avoid collision with the routes
// installed by routes() (/ws, /healthz, /). Connect's
// /talon.v1.* prefix is collision-free by construction.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Registry exposes the server's RPC registry so callers can wire
// adapters (like connectapi.Register) that delegate Connect calls
// back through the same handlers the WS path uses. One source of
// truth during the migration; no risk of two implementations
// drifting.
func (s *Server) Registry() *Registry { return s.registry }

// UptimeMs returns milliseconds since the server started. Exposed
// so Connect's InfraService.Health can answer without dispatching
// through the registry — the legacy "health" registry handler
// reads s.uptimeMs() via hc.Session.server, which only exists on
// a WS connection. Connect calls don't have a Session, so the
// nil-deref crashes.
func (s *Server) UptimeMs() int64 { return s.uptimeMs() }

// Version returns the server's version string. Lets Connect's
// InfraService.Health match the WS health payload exactly.
func (s *Server) Version() string { return serverVersion }

// Auth returns the server's auth config so the Connect path's auth
// interceptor can mirror the WS handshake's Authorize call without
// the connectapi package needing to know how cfg is wired. Returned
// by value — AuthConfig is small and immutable for a server's
// lifetime, so a copy is safe and avoids exposing the inner cfg.
func (s *Server) Auth() AuthConfig { return s.cfg.Auth }
