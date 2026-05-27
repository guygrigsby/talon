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

	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

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
