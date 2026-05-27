# 0009 Memory recall relevance floor (cosine threshold)

Status: Accepted

Date: 2026-05-26

## Context

The agent recalls irrelevant memories. Observed: user says "Oh hi!" and the
agent replies about "pizza" — a stored memory with no relation to the message.

Root cause (verified against `github.com/guygrigsby/jess`
@v0.0.0-20260525040818-2bdfd912fe1c):

- Recall takes the top-K nearest memories (K=8, hardcoded
  `memory/context_manager.go:82`) and injects all of them unconditionally as a
  leading "Relevant memories for this conversation" message
  (`context_manager.go:111-126`). The only gates are budget, kind-dedupe, and
  error-swallowing — **no relevance gate**.
- `ChromemStore.SearchVector` (`memory/store_chromem.go:195`) calls chromem's
  `QueryEmbedding`, which computes a cosine `Similarity` per result, but the
  loop builds entries via `metadataToEntry(r.ID, r.Metadata, r.Content)`
  (line 223) and **never reads `r.Similarity`**. The score is discarded before
  any jess code can filter on it.
- The embedder is not the problem: it's a real local MiniLM-L6-v2 (384-dim),
  the *same* instance at write and query time, so similarities are meaningful —
  they're just thrown away.

Net: on any input, the 8 nearest entries are injected regardless of how far
away they are. A small store guarantees off-topic hits ("pizza") ride along.

## Decision

Introduce an **absolute cosine relevance floor** in the recall pipeline, fixed
upstream in jess (per the no-fork / contribute-upstream rule), with talon
exposing the knob.

**Thread the score.** `ChromemStore.SearchVector` carries chromem's
`r.Similarity` onto each recalled `Entry` (new `Score float32` field, set only
on the vector path). This is the load-bearing change: nothing downstream can
filter on relevance until the score survives `SearchVector`.

**Apply the floor at the vector level, before fusion.** Drop entries whose
cosine `Score` is below a configured floor inside the vector search / recaller,
*before* `HybridRecaller`'s RRF fusion runs. RRF produces fused ranks, not
cosine scores, so a post-fusion threshold can't see the real distance — the
floor must gate the raw vector hits.

**K stays a cap, not the quality bar.** The floor decides relevance; K=8 (or
the configured budget) caps how many survivors are injected.

**Core / always-include memories bypass the floor.** Entries explicitly marked
always-include are not relevance-gated by design; only the "relevant" recall
path gets the floor.

**talon owns the knob.** A talon config key (e.g.
`memory.recall.min_score`, exact path settled in the plan) sets the floor and
is passed to jess when constructing the ContextManager. Ships with a sane
default (starting point ~0.30 for MiniLM cosine; tuned during implementation).
talon bumps the jess dependency to the version carrying the change.

## Consequences

- **Two repos.** The substantive change is a jess PR (score threading + floor +
  tests). talon gets a dependency bump, a config field, and wiring. During
  development talon uses a `replace` directive pointing at a local jess
  checkout; the talon change merges only after the jess version is published.
- **Small jess API change.** `Entry` gains a `Score` field; it's zero on
  non-vector paths (SimpleRecaller keyword/recency), which is acceptable — the
  floor only applies where a cosine score exists.
- **Default floor needs tuning.** Too high suppresses genuinely relevant
  recall; too low readmits noise. The plan includes a calibration step against
  real memories. The default is conservative and overridable.
- **Behavior change is intentional and observable.** After the fix, "Oh hi!"
  with no semantically close memory injects nothing, instead of the 8 nearest.
- **RRF interaction.** Applying the floor pre-fusion means the keyword ranker
  can still surface an exact-text match even at modest cosine score; the floor
  bounds the *vector* contribution, not lexical matches. Acceptable and arguably
  desirable; documented so it isn't mistaken for a leak.
- Future work: relative/top-gap trimming on top of the floor (deferred — see
  the threshold decision), and per-agent floor overrides.
