# 0002 Native Talon Config in TOML

Status: Accepted

Date: 2026-05-26

## Context

Before this migration, Talon read a large OpenClaw-shaped JSON config from
`~/.talon/talon.json`, with compatibility machinery for `~/.openclaw`. That
local config mixed gateway runtime settings, agents, model catalog data, plugin
auth, channels, memory, hooks, tools, wizard state, and migration metadata in
one document.

That shape keeps new Talon code coupled to old dotted JSON paths like
`agents.defaults.model.primary`, `models.providers`, `plugins.entries`, and
`channels.telegram`. It also pushes human-edited config into a format that is
hard to scan and easy to damage.

## Decision

Move Talon's primary human config to `~/.talon/config.toml` and treat the
OpenClaw JSON shape as migration input only.

Use Viper as the native config loading surface because Talon already uses Cobra
for command structure, and Viper gives us conventional file/env layering without
binding the design to a low-level TOML parser.

The native config should separate concerns:

- `config.toml`: stable human-owned settings for gateway, agents, models,
  tools, memory, channels, and plugin enablement.
- `credentials/` or OS keychain references: secret material and provider
  tokens. Human config should contain secret references, not literal secrets.
- `state/`: runtime-owned mutable state such as sessions, offsets, device
  pairing, wizard progress, caches, and generated metadata.
- `~/.talon` root: the main agent's memory-visible Markdown files
  (`IDENTITY.md`, `SOUL.md`, `AGENTS.md`, `USER.md`).
- `subagents/`: file-backed task/model profiles for delegation.
- `logs/` and `backups/`: operational artifacts, not config.

The agent model should be simplified. Talon has one main chat agent. That main
agent keeps its existing workspace and Markdown context files for now, including
`IDENTITY.md`, `SOUL.md`, and related files. Subagents are task/model profiles,
not separate personas with separate workspaces, identities, souls, or long-lived
state.

Runtime compatibility with the old server is not part of the native config
target. See ADR 0003 for the compatibility removal decision.

The Go runtime should expose typed config structs and domain-specific accessors
instead of scattering stringly typed dotted paths through chat, model, channel,
plugin, and CLI code.

## Consequences

This is a breaking internal migration. It should be staged:

1. Add a TOML loader/writer and typed native config model.
2. Add a one-shot migration from legacy JSON to `config.toml` plus state files.
3. Switch read paths to native config accessors.
4. Switch write paths away from generic dotted JSON mutation.
5. Remove fallback reads and compatibility docs after migration.

During the migration, preserve working chat, tool execution, RAG memory, and the
native Telegram plugin as first-class Talon features.
