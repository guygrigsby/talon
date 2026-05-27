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

	// Hybrid: token overlap catches keyword-exact hits, vector
	// catches semantic ones. RRF (K=60) fuses the two rankings.
	recaller := memory.NewHybridRecaller(
		memory.NewVectorRecaller(),
		memory.NewSimpleRecaller(),
	)

	return &server.MemoryConfig{
		Store:    store,
		Recaller: recaller,
	}
}

type memorySettings struct {
	Enabled bool
	Path    string
	Model   string
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
		Enabled: cfg.Memory.Enabled,
		Path:    cfg.Memory.Path,
		Model:   cfg.Memory.Model,
	}, nil
}
