package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/plugin"
)

const serverVersion = "0.1.0-dev"

type Config struct {
	Addr   string
	WebDir string
	Auth   AuthConfig
	Logger *log.Logger

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
}

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
	}
	chatStore := NewChatStore()
	sessionStore := NewSessionStore()
	s.chatStore = chatStore
	s.sessions_ = sessionStore
	if cfg.AgentResolver != nil && cfg.ProviderFactory != nil {
		ch := NewChatHandler(cfg.AgentResolver, cfg.ProviderFactory, chatStore).WithSessions(sessionStore)
		if cfg.WorkspaceResolver != nil && cfg.ToolRunnerFor != nil {
			// Wrap the user's factory so it always sees ch as the
			// SubagentRunner. ChatHandler implements RunInline, so it
			// satisfies the interface.
			factory := func(ws string) ToolRunner {
				return cfg.ToolRunnerFor(ws, ch)
			}
			ch = ch.WithTools(cfg.WorkspaceResolver, factory)
		}
		ch.Register(s.registry)
		s.chat = ch
	}
	if cfg.Paths.Talon.Dir != "" {
		s.reads = NewReadHandler(cfg.Paths)
		s.reads.Register(s.registry)
		NewImagesHandler(cfg.Paths).Register(s.registry)
		NewPluginDepsHandler(cfg.Paths).WithHost(cfg.PluginHost).Register(s.registry)
	}
	NewSessionsHandler(sessionStore, chatStore).Register(s.registry)
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/ws", s.handleWS)

	var staticHandler http.Handler
	if s.cfg.WebDir != "" {
		staticHandler = http.FileServer(http.Dir(s.cfg.WebDir))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			s.handleWS(w, r)
			return
		}
		if staticHandler != nil {
			staticHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
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

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
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
	s.logf("ws connected conn=%s remote=%s", sess.connID, r.RemoteAddr)
	sess.Run(r.Context())
	s.logf("ws closed conn=%s", sess.connID)
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
		s.logf("ws replaced conn=%s by conn=%s key=%q", old.connID, sess.connID, key)
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

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
