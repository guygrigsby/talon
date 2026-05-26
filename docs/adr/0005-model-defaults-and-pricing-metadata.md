# 0005 Model Defaults and Pricing Metadata

Status: Accepted

Date: 2026-05-26

## Context

The chat inspector needs active-model pricing, but asking users to enter token
prices into config is brittle. The Models tab also needs an operational way to
change defaults without editing TOML manually.

## Decision

Keep model identity, aliases, and the main default model in Talon's config. The
Models tab writes `agents.defaults.model.primary` for the main default and
continues writing aliases under `agents.defaults.models.<provider/model>.alias`.

Pricing is resolved in this order:

1. Catalog or plugin-provided model `cost` metadata.
2. Explicit config override, when present.
3. A curated built-in table for common OpenAI, Anthropic, and DeepSeek models.

The built-in table is local and versioned with Talon. It uses USD per million
tokens and is intended for display and cost-cap guardrails, not billing.

## Consequences

The right chat sidebar can show active-model pricing without requiring config
price entries. Unknown models remain usable and show no price until a catalog,
plugin, override, or built-in entry supplies one.

Subagent model defaults remain in subagent file front matter so task-specific
model choices are reviewed with the subagent prompt.
