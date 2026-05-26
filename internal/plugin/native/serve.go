package native

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"sync"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// Serve is the plugin-side entry point. Each first-party plugin's
// `talon plugin run <name>` dispatcher calls native.Serve("<name>",
// impl) and never returns. The host has set TALON_PLUGIN_HANDSHAKE
// via go-plugin's magic-cookie env; missing/wrong cookie prints
// go-plugin's handshake-help message and exits non-zero.
func Serve(name string, srv pb.PluginServer) {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	logger := slog.With("plugin", name)

	gp := &grpcPlugin{Impl: srv}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			1: {PluginMapKey: gp},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     newHCLogAdapter(logger),
	})
}

// HostClientHolder is a goroutine-safe slot for the pb.HostClient a
// plugin uses to call back into the host. Plugins embed
// *HostClientHolder into their pb.PluginServer implementation and
// call SetFromBroker inside Initialize once the host has told them
// the broker id. Until then, Get returns nil.
type HostClientHolder struct {
	mu sync.Mutex
	c  pb.HostClient
}

func (h *HostClientHolder) Get() pb.HostClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.c
}

// SetFromBroker dials the broker id the host sent in
// InitializeRequest.HostBrokerId and caches the resulting
// pb.HostClient. Safe to call from a plugin's Initialize handler.
// Returns nil if brokerID is 0 so plugins can call SetFromBroker
// unconditionally in tests or minimal hosts.
func (h *HostClientHolder) SetFromBroker(brokerID int64) error {
	if brokerID == 0 {
		return nil
	}
	b := currentBroker()
	if b == nil {
		return fmt.Errorf("native: no broker captured (Serve not called?)")
	}
	conn, err := b.Dial(uint32(brokerID))
	if err != nil {
		return fmt.Errorf("native: broker dial %d: %w", brokerID, err)
	}
	h.mu.Lock()
	h.c = pb.NewHostClient(conn)
	h.mu.Unlock()
	return nil
}

// currentBroker / setCurrentBroker bridge the plugin-side broker
// captured in grpcPlugin.GRPCServer to HostClientHolder.
// Set once per plugin process, before goplugin.Serve returns.
var (
	currentBrokerMu sync.Mutex
	curBroker       *goplugin.GRPCBroker
)

func setCurrentBroker(b *goplugin.GRPCBroker) {
	currentBrokerMu.Lock()
	defer currentBrokerMu.Unlock()
	curBroker = b
}

func currentBroker() *goplugin.GRPCBroker {
	currentBrokerMu.Lock()
	defer currentBrokerMu.Unlock()
	return curBroker
}

// newHCLogAdapter routes go-plugin's hclog output through talon's
// slog logger. Without this, go-plugin emits its own JSON to stderr,
// which fights with talon's slog handler.
func newHCLogAdapter(s *slog.Logger) hclog.Logger {
	return &hclogToSlog{s: s}
}

type hclogToSlog struct {
	s    *slog.Logger
	name string
}

func (h *hclogToSlog) log(level slog.Level, msg string, args ...any) {
	h.s.Log(context.Background(), level, msg, args...)
}

func (h *hclogToSlog) Log(level hclog.Level, msg string, args ...any) {
	h.log(hclogToSlogLevel(level), msg, args...)
}

func hclogToSlogLevel(l hclog.Level) slog.Level {
	switch l {
	case hclog.Trace, hclog.Debug:
		return slog.LevelDebug
	case hclog.Info:
		return slog.LevelInfo
	case hclog.Warn:
		return slog.LevelWarn
	case hclog.Error:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (h *hclogToSlog) Trace(msg string, args ...any) { h.log(slog.LevelDebug, msg, args...) }
func (h *hclogToSlog) Debug(msg string, args ...any) { h.log(slog.LevelDebug, msg, args...) }
func (h *hclogToSlog) Info(msg string, args ...any)  { h.log(slog.LevelInfo, msg, args...) }
func (h *hclogToSlog) Warn(msg string, args ...any)  { h.log(slog.LevelWarn, msg, args...) }
func (h *hclogToSlog) Error(msg string, args ...any) { h.log(slog.LevelError, msg, args...) }

func (h *hclogToSlog) IsTrace() bool { return h.s.Enabled(context.Background(), slog.LevelDebug) }
func (h *hclogToSlog) IsDebug() bool { return h.s.Enabled(context.Background(), slog.LevelDebug) }
func (h *hclogToSlog) IsInfo() bool  { return h.s.Enabled(context.Background(), slog.LevelInfo) }
func (h *hclogToSlog) IsWarn() bool  { return h.s.Enabled(context.Background(), slog.LevelWarn) }
func (h *hclogToSlog) IsError() bool { return h.s.Enabled(context.Background(), slog.LevelError) }

func (h *hclogToSlog) ImpliedArgs() []any { return nil }
func (h *hclogToSlog) With(args ...any) hclog.Logger {
	return &hclogToSlog{s: h.s.With(args...), name: h.name}
}
func (h *hclogToSlog) Name() string { return h.name }
func (h *hclogToSlog) Named(name string) hclog.Logger {
	if h.name != "" {
		name = h.name + "." + name
	}
	return &hclogToSlog{s: h.s.With("named", name), name: name}
}
func (h *hclogToSlog) ResetNamed(name string) hclog.Logger {
	return &hclogToSlog{s: h.s, name: name}
}
func (h *hclogToSlog) SetLevel(hclog.Level)  {}
func (h *hclogToSlog) GetLevel() hclog.Level { return hclog.Info }

func (h *hclogToSlog) StandardLogger(*hclog.StandardLoggerOptions) *log.Logger {
	return log.New(io.Discard, "", 0)
}
func (h *hclogToSlog) StandardWriter(*hclog.StandardLoggerOptions) io.Writer {
	return io.Discard
}
