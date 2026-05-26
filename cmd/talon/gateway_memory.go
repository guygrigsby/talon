package main

// Memory sidecar wiring for `talon gateway run` (talon-2dn).
//
// Constructs jess/memory pieces only when the merged config has
// `memory.enabled: true`. Disabled (default) skips the whole
// thing — no embedder download, no chromem allocation. Path is
// `~/.talon/memory/` by default; override via `memory.path`.

import (
	"log/slog"
	"path/filepath"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/memory/embed/gomlx"
	"github.com/tidwall/gjson"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/server"
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
	merged, err := config.MergedBytes(paths)
	if err != nil {
		slog.Debug("memory: cannot read merged config; skipping sidecar", "err", err)
		return nil
	}
	if !gjson.GetBytes(merged, "memory.enabled").Bool() {
		// Default disabled. User opts in via:
		//   talon config set memory.enabled true
		return nil
	}

	storePath := gjson.GetBytes(merged, "memory.path").Str
	if storePath == "" {
		storePath = filepath.Join(paths.Talon.Dir, "memory")
	}

	// Embedder model: defaults to gomlx.DefaultModel (MiniLM-L6-v2,
	// 90MB, 384-dim). Override via memory.model (a HuggingFace repo
	// ID) — Dim+SeqLen auto-detected from the repo's config.json.
	embedOpts := gomlx.Options{}
	if modelID := gjson.GetBytes(merged, "memory.model").Str; modelID != "" {
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
