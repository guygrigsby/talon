package main

// Memory sidecar wiring for `talon gateway run` (talon-2dn).
//
// Constructs jess/memory pieces only when the native config has
// `memory.enabled: true`. Disabled (default) skips the whole
// thing — no embedder download, no chromem allocation. Path is
// `~/.talon/memory/` by default; override via `memory.path`.

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/memory/embed/gomlx"
	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/audit"
	"github.com/guygrigsby/talon/internal/claudemem"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// defaultClaudeInjectBytes caps the Claude-memory index folded into the
// system prompt when memory.claude.max_inject_bytes is unset (ADR 0013).
const defaultClaudeInjectBytes = 4096

// defaultClaudeReadBytes bounds a single claude_memory tool read. Larger
// than the inject cap because a deliberate tool read wants the full
// entry, but still bounded so one read can't blow the context.
const defaultClaudeReadBytes = 32768

type claudeMemorySettings struct {
	Enabled        bool
	Path           string
	Inject         bool
	MaxInjectBytes int
}

// readClaudeMemorySettings reads memory.claude.* from native config,
// applying the nil-means-default semantics: enabled defaults false,
// inject defaults true. A missing config file yields the defaults.
func readClaudeMemorySettings(paths talonpath.Paths) (claudeMemorySettings, error) {
	defaults := claudeMemorySettings{Inject: true, MaxInjectBytes: defaultClaudeInjectBytes}
	if _, err := os.Stat(paths.Talon.Config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return defaults, err
	}
	cfg, err := talonconfig.LoadFile(paths.Talon.Config)
	if err != nil {
		return defaults, err
	}
	c := cfg.Memory.Claude
	out := defaults
	if c.Enabled != nil {
		out.Enabled = *c.Enabled
	}
	out.Path = c.Path
	if c.Inject != nil {
		out.Inject = *c.Inject
	}
	if c.MaxInjectBytes > 0 {
		out.MaxInjectBytes = int(c.MaxInjectBytes)
	}
	return out, nil
}

// buildClaudeMemory resolves memory.claude.* and, when enabled with a
// valid dir, returns the (capped) index to inject and the path-confined
// claude_memory tool (ADR 0013). ok=false means the feature is off or
// inert. Like buildMemorySidecar, failures log + return ok=false rather
// than aborting startup. index is empty when inject is disabled.
func buildClaudeMemory(paths talonpath.Paths) (index string, claudeTool tool.Tool, ok bool) {
	settings, err := readClaudeMemorySettings(paths)
	if err != nil {
		slog.Debug("claude-memory: cannot read native config; skipping", "err", err)
		return "", nil, false
	}
	if !settings.Enabled {
		// Default off. Opt in via: talon config set memory.claude.enabled true
		return "", nil, false
	}
	if settings.Path == "" {
		slog.Warn("claude-memory: enabled but memory.claude.path is unset; feature inert")
		return "", nil, false
	}
	store, err := claudemem.New(settings.Path)
	if err != nil {
		slog.Error("claude-memory: cannot open memory dir; feature disabled",
			"path", settings.Path, "err", err)
		return "", nil, false
	}
	if settings.Inject {
		idx, err := store.Index(settings.MaxInjectBytes)
		if err != nil {
			slog.Error("claude-memory: cannot read index; injecting nothing",
				"path", settings.Path, "err", err)
		} else {
			index = idx
		}
	}
	claudeTool = claudemem.NewTool(store, defaultClaudeReadBytes)
	slog.Info("claude-memory enabled", "path", store.Dir(), "inject", settings.Inject)
	return index, claudeTool, true
}

// defaultRecallMinScore is the absolute cosine relevance floor applied
// to vector recall when memory.recall.min_score is unset. Tuned for
// MiniLM-L6-v2 cosine: below this, recalled memories are noise (the
// "Oh hi!" -> pizza problem). Override via
// `talon config set memory.recall.min_score <f>`.
const defaultRecallMinScore = 0.30

// buildMemorySidecar returns a configured MemoryConfig or nil when
// memory is disabled or initialization fails. nil is the no-op
// signal the caller hands to ChatHandler.WithMemory.
//
// Failures are logged + return nil rather than aborting startup:
// a broken memory layer should not prevent the gateway from
// answering chat.send. Users get plain (no-memory) behavior and
// a log line pointing at the cause.
func buildMemorySidecar(paths talonpath.Paths) *server.MemoryConfig {
	settings, err := readMemorySettings(paths)
	if err != nil {
		slog.Debug("memory: cannot read native config; skipping sidecar", "err", err)
		return nil
	}
	if !settings.Enabled {
		// Default disabled. User opts in via:
		//   talon config set memory.enabled true
		return nil
	}

	storePath := settings.Path
	if storePath == "" {
		storePath = filepath.Join(paths.Talon.Dir, "memory")
	}

	// Embedder model: defaults to gomlx.DefaultModel (MiniLM-L6-v2,
	// 90MB, 384-dim). Override via memory.model (a HuggingFace repo
	// ID) — Dim+SeqLen auto-detected from the repo's config.json.
	embedOpts := gomlx.Options{}
	if modelID := settings.Model; modelID != "" {
		embedOpts.ModelID = modelID
	}

	emb, err := gomlx.NewEmbedder(embedOpts)
	if err != nil {
		slog.Error("memory: embedder init failed; sidecar disabled", "err", err)
		return nil
	}

	store, err := memory.NewChromemStore(emb, memory.ChromemOptions{
		Path:           storePath,
		CollectionName: "talon",
	})
	if err != nil {
		slog.Error("memory: chromem store init failed; sidecar disabled",
			"path", storePath, "err", err)
		return nil
	}

	return &server.MemoryConfig{
		Store:    store,
		Recaller: buildRecaller(resolveRecallMinScore(settings)),
	}
}

type memorySettings struct {
	Enabled        bool
	Path           string
	Model          string
	RecallMinScore float64
}

// resolveRecallMinScore returns the configured vector-recall floor, or
// the code default when unset (zero). A positive configured value
// always wins.
func resolveRecallMinScore(s memorySettings) float64 {
	if s.RecallMinScore > 0 {
		return s.RecallMinScore
	}
	return defaultRecallMinScore
}

// buildRecaller constructs the production recall pipeline: a hybrid of a
// vector recaller (cosine floor at minScore) and a keyword recaller gated to
// require a lexical match. Both paths are relevance-gated so a bare greeting
// doesn't surface unrelated memories. Shared by the gateway and tests so the
// tested config can't drift from production.
func buildRecaller(minScore float64) memory.Recaller {
	return memory.NewHybridRecaller(
		// Token overlap catches keyword-exact hits, vector catches
		// semantic ones; RRF (K=60) fuses the two rankings.
		memory.NewVectorRecaller(memory.WithMinScore(float32(minScore))),
		// RequireMatch drops zero-signal hits; stopwords drop common
		// query glue. "user"/"talon" are added to the English defaults
		// because talon's memories are user-centric ("User likes X"),
		// so those tokens otherwise match nearly everything.
		memory.NewSimpleRecaller(
			memory.WithRequireMatch(),
			memory.WithStopwords(recallStopwords...),
		),
	)
}

// recallStopwords is the English default set plus talon domain terms that
// are ubiquitous in memories and thus low-signal for keyword recall.
var recallStopwords = append(append([]string{}, memory.DefaultStopwords...),
	"user", "talon", "gateway")

// buildAuditRecorder constructs the agent-action audit recorder (ADR 0011)
// when audit.enabled is true (the default). Returns nil when disabled or
// when the recorder can't be created — a failed audit setup must never take
// down the gateway, so errors are logged and swallowed.
func buildAuditRecorder(paths talonpath.Paths) audit.Recorder {
	cfg, err := readAuditSettings(paths)
	if err != nil {
		slog.Debug("audit: cannot read native config; using defaults", "err", err)
	}
	if !cfg.enabled {
		slog.Info("audit log disabled (audit.enabled=false)")
		return nil
	}
	path := paths.Talon.AgentAuditLogPath()
	rec, err := audit.NewJSONLRecorder(audit.Options{
		Path:      path,
		MaxSizeMB: cfg.maxSizeMB,
		Keep:      int(cfg.keep),
	})
	if err != nil {
		slog.Error("audit: recorder init failed; audit log disabled", "path", path, "err", err)
		return nil
	}
	slog.Info("audit log enabled", "path", path)
	return rec
}

type auditSettings struct {
	enabled   bool
	maxSizeMB int64
	keep      int64
}

// readAuditSettings reads audit.* from native config. A missing config file
// yields the defaults (enabled). Enabled defaults to true when unset.
func readAuditSettings(paths talonpath.Paths) (auditSettings, error) {
	defaults := auditSettings{enabled: true}
	if _, err := os.Stat(paths.Talon.Config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return defaults, err
	}
	cfg, err := talonconfig.LoadFile(paths.Talon.Config)
	if err != nil {
		return defaults, err
	}
	out := auditSettings{
		enabled:   true,
		maxSizeMB: cfg.Audit.MaxSizeMB,
		keep:      cfg.Audit.Keep,
	}
	if cfg.Audit.Enabled != nil {
		out.enabled = *cfg.Audit.Enabled
	}
	return out, nil
}

func readMemorySettings(paths talonpath.Paths) (memorySettings, error) {
	if _, err := os.Stat(paths.Talon.Config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return memorySettings{}, nil
		}
		return memorySettings{}, err
	}
	cfg, err := talonconfig.LoadFile(paths.Talon.Config)
	if err != nil {
		return memorySettings{}, err
	}
	return memorySettings{
		Enabled:        cfg.Memory.Enabled,
		Path:           cfg.Memory.Path,
		Model:          cfg.Memory.Model,
		RecallMinScore: cfg.Memory.Recall.MinScore,
	}, nil
}
