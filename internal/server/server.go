package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/guygrigsby/talon/internal/openclaw"
	plugin "github.com/guygrigsby/talon/internal/plugin/legacy"
	"github.com/guygrigsby/talon/web"
)

const serverVersion = "0.1.0-dev"

type Config struct {
	Addr   string
	WebDir string
	Auth   AuthConfig

	// AgentResolver, if set, enables chat.send by translating sessionKey →
	// agent → ModelID. Leaving nil disables chat.send (returns INTERNAL).
	AgentResolver AgentResolver
	// ProviderFactory pairs with AgentResolver to materialize provider
	// implementations for the agent's primary model.
	ProviderFactory ProviderFactory
	// Paths, when set, enables the read-only RPCs sourced from the
	// merged config (agents.list, models.list, config.schema). Leaving
	// it as the zero value disables those handlers.
	Paths openclaw.Paths
	// WorkspaceResolver, paired with ToolRunnerFor, enables tool calling
	// in chat.send. Leaving either nil keeps chat in text-only mode.
	WorkspaceResolver WorkspaceResolver
	// ToolRunnerFor returns a per-agent ToolRunner given a workspace
	// directory and the ChatHandler-backed SubagentRunner. The factory
	// is called once per chat.send. Pass the runner through to
	// tools.NewWithSubagent (or equivalent) so the model can delegate to
	// other agents.
	ToolRunnerFor func(workspace string, sub SubagentRunner) ToolRunner
	// PluginHost, when set, lets read-only RPCs surface the loaded
	// plugin set (plugins.list). Optional — leaving it nil keeps the
	// /plugins UI's "Loaded plugins" section empty without breaking
	// anything else.
	PluginHost *plugin.Host
}

// SubagentRunner is the indirection the tool registry uses to dispatch
// the `subagent` tool back into a parent ChatHandler's multi-turn loop.
// Same shape as tools.SubagentRunner — Go's structural typing means a
// ChatHandler value satisfies both with no explicit conversion.
type SubagentRunner interface {
	RunInline(ctx context.Context, agentID, message string) (string, error)
}

type Server struct {
	cfg       Config
	mux       *http.ServeMux
	registry  *Registry
	startedAt time.Time

	// Handler instances retained so cmd/talon can wire other surfaces
	// (the gRPC plugin Host service) against the same in-process
	// ChatStore/SessionStore as the WS surface — without these, plugins
	// would see a different view of session state than the UI does.
	chat       *ChatHandler
	chatStore  *ChatStore
	sessions_  *SessionStore // trailing _ to avoid collision with the field below
	reads      *ReadHandler
	cron       *CronHandler

	// sessions tracks active authenticated sessions keyed by
	// "<clientId>|<instanceId>" (only registered when both are
	// non-empty). When a new connect handshake completes with a key
	// already in the map, the older session is closed with a structured
	// reason. This guards against the openclaw web UI's habit of
	// occasionally opening two simultaneous WS connections from a single
	// page load (Lit re-mount, HMR, or a connectGateway race during
	// bootstrap).
	sessionsMu sync.Mutex
	sessions   map[string]*Session

	// sinks fans server-pushed events out to additional subscribers
	// (Connect's ChatService.Subscribe today; debug taps in future).
	// The WS path still receives events the direct way via
	// Session.PushEvent — broadcasting is purely additive so the
	// legacy path stays exactly as it was. nil-safe; methods no-op
	// when the registry hasn't been wired.
	sinks *SinkRegistry
}

// Sinks returns the server's event sink registry so callers
// outside this package (notably internal/connectapi) can subscribe
// to a session-key's event stream without touching internal state.
// Always non-nil after server.New returns.
func (s *Server) Sinks() *SinkRegistry { return s.sinks }

// ChatHandler returns the in-process chat handler. nil if Config.AgentResolver
// or ProviderFactory was not set (chat disabled). Used by the Host service
// to wire RunSubagent.
func (s *Server) ChatHandler() *ChatHandler { return s.chat }

// ChatStore returns the shared in-memory chat history store. Always
// non-nil. Used by the Host service to surface GetChatHistory to plugins
// against the same data the WS chat.history sees.
func (s *Server) ChatStore() *ChatStore { return s.chatStore }

// SessionStore returns the shared per-session prefs store (model
// overrides, etc). Always non-nil.
func (s *Server) SessionStore() *SessionStore { return s.sessions_ }

// ReadHandler returns the shared read-only RPC handler (or a fresh one
// bound to Paths if reads weren't pre-registered). Always non-nil.
func (s *Server) ReadHandler() *ReadHandler {
	if s.reads != nil {
		return s.reads
	}
	return NewReadHandler(s.cfg.Paths)
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		registry:  NewRegistry(),
		startedAt: time.Now(),
		sessions:  make(map[string]*Session),
		sinks:     NewSinkRegistry(),
	}
	chatStore := NewChatStore()
	sessionStore := NewSessionStore()
	s.chatStore = chatStore
	s.sessions_ = sessionStore
	if cfg.AgentResolver != nil && cfg.ProviderFactory != nil {
		ch := NewChatHandler(cfg.AgentResolver, cfg.ProviderFactory, chatStore).
			WithSessions(sessionStore).
			WithSinks(s.sinks)
		if cfg.WorkspaceResolver != nil && cfg.ToolRunnerFor != nil {
			// Wrap the user's factory so it always sees ch as the
			// SubagentRunner. ChatHandler implements RunInline, so it
			// satisfies the interface.
			factory := func(ws string) ToolRunner {
				return cfg.ToolRunnerFor(ws, ch)
			}
			ch = ch.WithTools(cfg.WorkspaceResolver, factory)
		}
		// Per-agent daily USD cap (agents.defaults.dailyUsdCap).
		// No-op when the cap is unset.
		if cfg.Paths.Talon.Dir != "" {
			ch = ch.WithCostTracker(NewCostTracker(cfg.Paths))
		}
		ch.Register(s.registry)
		s.chat = ch
	}
	if cfg.Paths.Talon.Dir != "" {
		s.reads = NewReadHandler(cfg.Paths)
		s.reads.Register(s.registry)
		NewPluginDepsHandler(cfg.Paths).WithHost(cfg.PluginHost).Register(s.registry)
		NewChannelsSetupHandler(cfg.Paths).Register(s.registry)
		// Cron scheduler: jobs persist under ~/.talon/cron/, dispatch
		// fires through the same Registry so any session-agnostic RPC
		// can be scheduled. Constructor failures (corrupt jobs.json)
		// are logged and swallowed so a bad on-disk state doesn't
		// take down the whole gateway.
		if ch, err := NewCronHandler(cfg.Paths, dispatchFromRegistry(s.registry)); err != nil {
			slog.Error("cron handler init failed; cron RPCs disabled", "err", err)
		} else {
			ch.Register(s.registry)
			s.cron = ch
		}
	}
	NewSessionsHandler(sessionStore, chatStore).Register(s.registry)
	NewNodesHandler().Register(s.registry)
	NewCommandsHandler().Register(s.registry)
	NewModelsAuthStatusHandler(cfg.Paths).Register(s.registry)
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/ws", s.handleWS)

	staticFS, spaFallback := s.resolveStaticFS()
	var staticHandler http.Handler
	if staticFS != nil {
		staticHandler = http.FileServer(http.FS(staticFS))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			s.handleWS(w, r)
			return
		}
		if staticHandler == nil {
			http.NotFound(w, r)
			return
		}
		if spaFallback && shouldServeSPAFallback(staticFS, r) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			staticHandler.ServeHTTP(w, r2)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})
}

// resolveStaticFS picks the FS that backs the static handler.
//
// Priority: explicit --web <dir> (operator override) → embedded SvelteKit
// build (default in a freshly-built binary) → nil (no UI shipped). The SPA
// fallback flag is only enabled for the embedded build, since an arbitrary
// --web directory may not be an SPA.
func (s *Server) resolveStaticFS() (fs.FS, bool) {
	if s.cfg.WebDir != "" {
		return os.DirFS(s.cfg.WebDir), false
	}
	if web.HasIndex() {
		return web.Assets(), true
	}
	return nil, false
}

// shouldServeSPAFallback returns true when the request looks like a
// client-side route (no file extension, GET) and the embedded FS has no
// matching file. In that case the SPA's index.html is served instead so
// SvelteKit's client router can take over.
func shouldServeSPAFallback(staticFS fs.FS, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		return false
	}
	if strings.Contains(path.Base(p), ".") {
		return false
	}
	if _, err := fs.Stat(staticFS, p); err == nil {
		return false
	}
	return true
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- hs.ListenAndServe() }()

	// Cron scheduler ticks once a second. Granular enough that a job
	// scheduled for "16:30" fires within the right minute even with
	// system clock jitter; sparse enough that idle CPU cost is
	// negligible at the scale of a personal gateway.
	if s.cron != nil {
		s.cron.Service().Start(ctx, time.Second)
		defer s.cron.Service().Stop()
	}

	select {
	case <-ctx.Done():
		// Broadcast drain to in-flight Connect Subscribe handlers
		// FIRST so they unblock and return. http.Server.Shutdown
		// otherwise waits its whole timeout for each stream to
		// time out individually — Ctrl-C felt like 5+ seconds of
		// hang before this. Also force-close any active WS
		// sessions; Shutdown doesn't track hijacked conns and
		// they'd hold the process up indefinitely otherwise.
		s.sinks.Close()
		s.closeAllSessions("gateway shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// closeAllSessions issues a normal-closure on every registered WS
// session. Called from the shutdown path so Ctrl-C drains within
// the http.Server.Shutdown window rather than hitting the timeout.
// Sessions deregister themselves from Session.Run's defer chain, so
// we just trigger the close; the map clean-up happens for free.
func (s *Server) closeAllSessions(reason string) {
	s.sessionsMu.Lock()
	snapshot := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		snapshot = append(snapshot, sess)
	}
	s.sessionsMu.Unlock()
	for _, sess := range snapshot {
		sess.shutdown(reason)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"service":  "talon-gateway",
		"version":  serverVersion,
		"uptimeMs": s.uptimeMs(),
		"authMode": string(s.cfg.Auth.Mode),
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 * 1024 * 1024)

	sess := newSession(s, conn)
	slog.Info("ws connected", "conn", sess.connID, "remote", r.RemoteAddr)
	sess.Run(r.Context())
	slog.Info("ws closed", "conn", sess.connID)
}

func (s *Server) uptimeMs() int64 {
	return time.Since(s.startedAt).Milliseconds()
}

// registerSession swaps sess into the sessions map under key. If a prior
// session was registered there, it's closed with a structured reason — the
// caller (Session.handshake) does this immediately after hello-ok so the
// stale half of a duplicate-connect race exits cleanly.
//
// Returns the displaced session (nil if none) so the caller can wait for
// it to drain if needed.
func (s *Server) registerSession(key string, sess *Session) *Session {
	if key == "" {
		return nil
	}
	s.sessionsMu.Lock()
	old := s.sessions[key]
	s.sessions[key] = sess
	s.sessionsMu.Unlock()
	if old != nil && old != sess {
		slog.Info("ws session replaced",
			"old_conn", old.connID, "new_conn", sess.connID, "key", key)
		old.shutdown("replaced-by-newer-instance")
	}
	return old
}

// unregisterSession removes sess from the map iff it's still the active
// entry under key. Compare-and-delete avoids the case where the
// just-displaced session deregisters its successor on the way out.
func (s *Server) unregisterSession(key string, sess *Session) {
	if key == "" {
		return
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.sessions[key] == sess {
		delete(s.sessions, key)
	}
}

