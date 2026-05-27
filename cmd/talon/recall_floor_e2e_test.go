package main

import (
	"context"
	"strings"
	"testing"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/memory/embed/gomlx"
)

// TestRecallRelevanceFloor_E2E exercises the REAL embedder (MiniLM-L6-v2) +
// chromem + the production recaller config so the cosine relevance floor is
// validated against real semantics, not the keyword test stub. Gated like
// jess's embedder E2E test because it loads the ~90MB model.
//
//	TALON_EMBEDDER_E2E=1 go test ./cmd/talon -run TestRecallRelevanceFloor_E2E -v
//
// Reproduces the "pizza on hi" bug: a bare greeting must not pull in
// unrelated memories, while an on-topic query still recalls the right one.
func TestRecallRelevanceFloor_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E embedder test in -short")
	}

	emb, err := gomlx.NewEmbedder(gomlx.Options{})
	if err != nil {
		t.Skipf("embedder unavailable (model not cached?): %v", err)
	}
	store, err := memory.NewChromemStore(emb, memory.ChromemOptions{
		Path:           t.TempDir(),
		CollectionName: "talon-test",
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	const agent = "main"
	mems := []string{
		"User ordered a pepperoni pizza on Friday night.",
		"User's gateway runs on a Tailscale tailnet.",
		"User prefers dark mode in the UI.",
		"Deploys go out on Fridays after the test suite passes.",
	}
	for _, m := range mems {
		if _, err := store.Append(context.Background(), memory.Entry{Text: m, AgentID: agent}); err != nil {
			t.Fatalf("append %q: %v", m, err)
		}
	}

	ctx := context.Background()

	// Production recaller, via the same builder gateway_memory.go uses —
	// so this test can't drift from the real config.
	prod := buildRecaller(defaultRecallMinScore)

	// Diagnostic: raw vector scores for the greeting, so we can calibrate.
	rawVec, _ := memory.NewVectorRecaller().Recall(ctx, store, agent, "Oh hi!", 8)
	for _, e := range rawVec {
		t.Logf("greeting vector score: %.3f  %q", e.Score, e.Text)
	}

	t.Run("greeting recalls nothing relevant", func(t *testing.T) {
		got, err := prod.Recall(ctx, store, agent, "Oh hi!", 8)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range got {
			t.Logf("LEAKED on 'Oh hi!': %q (score %.3f)", e.Text, e.Score)
		}
		if len(got) != 0 {
			t.Fatalf("greeting should recall no memories, got %d (irrelevant recall not gated)", len(got))
		}
	})

	t.Run("on-topic query still recalls", func(t *testing.T) {
		got, err := prod.Recall(ctx, store, agent, "what food did the user order recently?", 8)
		if err != nil {
			t.Fatal(err)
		}
		var hit bool
		for _, e := range got {
			t.Logf("recalled: %q (score %.3f)", e.Text, e.Score)
			lower := strings.ToLower(e.Text)
			if strings.Contains(lower, "pizza") {
				hit = true
				continue
			}
			// Stopwords ("user"/"the") + RequireMatch should keep the
			// unrelated memories out — they only matched on common glue.
			for _, bad := range []string{"dark mode", "deploys", "tailnet"} {
				if strings.Contains(lower, bad) {
					t.Errorf("over-match: irrelevant memory recalled on food query: %q", e.Text)
				}
			}
		}
		if !hit {
			t.Fatalf("on-topic food query should recall the pizza memory; got %d entries", len(got))
		}
	})
}
