# 0003 Remove OpenClaw Compatibility

Status: Accepted

Date: 2026-05-26

## Context

Talon started as an OpenClaw-compatible gateway so it could reuse existing UI
protocols, config shape, plugin metadata, and bundled JavaScript extensions.
That phase delivered useful working surfaces: chat, tools, RAG memory, native
gRPC plugins, Telegram, and an embedded control UI.

The compatibility layer is now a liability. It keeps Talon coupled to
OpenClaw's JSON config, `~/.openclaw` fallback paths, Node plugin shim, bundled
extension tree, `openclaw.plugin.json` metadata, and multi-workspace subagent
semantics. Those branches make the code harder to reason about and preserve
paradigms Talon no longer wants: separate subagent identities, souls,
workspaces, and runtime surfaces shaped by OpenClaw rather than by Talon.

## Decision

Remove OpenClaw compatibility as a runtime architecture target. Talon reads and
writes `~/.talon/config.toml`, uses Talon-owned state paths, and runs plugins
through the native gRPC plugin host. Third-party plugin binaries live under
`~/.talon/plugins` or an explicit `plugins.load_paths` entry.

The only supported OpenClaw-specific surface is one-way migration:
`talon config migrate-toml [openclaw-json-file]` may parse OpenClaw JSON and
print a Talon TOML preview. The parser may keep OpenClaw names where they make
the migration contract clear. No OpenClaw path, Node shim, bundled JS
extension, plugin metadata file, or API compatibility shim is a Talon runtime
surface.

Subagents are Talon task/model profiles stored as files under
`~/.talon/subagents`, not OpenClaw-style agents with separate workspaces,
identities, souls, or long-lived state. The main chat agent remains the
coordinator and may keep its current Markdown context files.

## Consequences

This is an intentional breaking change. Docker images no longer include Node or
bundled extension assets. The gateway spawns first-party plugins through
`talon plugin run <name>`. Users who still need old plugins must port them to
Talon's gRPC plugin protocol.

Tests and docs should describe Talon behavior directly. OpenClaw names may
remain only in migration code, migration tests, ADR history, or where an
external wire field still intentionally carries that spelling during a planned
transition.
