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
	"time"

	"github.com/guygrigsby/talon/internal/talonpath"
	plugin "github.com/guygrigsby/talon/internal/plugin/host"
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
	Paths talonpath.Paths
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
	// ChatStore/SessionStore as the Connect surface — without these
	// shared references, plugins would see a different view of
	// session state than the UI does.
	chat      *ChatHandler
	chatStore *ChatStore
	sessions_ *SessionStore // kept name from the WS era
	reads     *ReadHandler
	cron      *CronHandler

	// sinks fans every server-pushed event out to subscribers of
	// a session-key. The ChatService.Subscribe handler in
	// internal/connectapi registers one sink per active client.
	// nil-safe; methods no-op when the registry hasn't been wired.
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
		s.reads = NewReadHandler(cfg.Paths).WithHost(cfg.PluginHost)
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

	staticFS, spaFallback := s.resolveStaticFS()
	var staticHandler http.Handler
	if staticFS != nil {
		staticHandler = http.FileServer(http.FS(staticFS))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
// fallback is enabled whenever the selected filesystem has index.html, which
// covers both embedded builds and rebuilt local --web directories.
func (s *Server) resolveStaticFS() (fs.FS, bool) {
	if s.cfg.WebDir != "" {
		staticFS := os.DirFS(s.cfg.WebDir)
		return staticFS, hasStaticIndex(staticFS)
	}
	if web.HasIndex() {
		return web.Assets(), true
	}
	return nil, false
}

func hasStaticIndex(staticFS fs.FS) bool {
	_, err := fs.Stat(staticFS, "index.html")
	return err == nil
}

// shouldServeSPAFallback returns true when the request looks like a
// client-side route (no file extension, GET) and the embedded FS has no
// matching file. In that case the SPA's index.html is served instead so
// SvelteKit's client router can take over.
func shouldServeSPAFallback(staticFS fs.FS, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if isBackendRoute(r.URL.Path) {
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

func isBackendRoute(requestPath string) bool {
	switch {
	case requestPath == "/healthz":
		return true
	case strings.HasPrefix(requestPath, "/talon.v1."):
		return true
	default:
		return false
	}
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
		// hang before this.
		s.sinks.Close()
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

func (s *Server) uptimeMs() int64 {
	return time.Since(s.startedAt).Milliseconds()
}
