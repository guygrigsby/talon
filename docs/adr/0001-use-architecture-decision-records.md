# 0001 Use Architecture Decision Records

Status: Accepted

Date: 2026-05-26

## Context

Talon has accumulated compatibility code, config conventions, and migration
choices from its OpenClaw-compatible phase. Those choices are now scattered
across code comments, tests, docs, and local config paths, which makes it hard
to tell which decisions still apply to Talon as a standalone agentic server.

## Decision

Record durable architecture decisions in `docs/adr/` using numbered Markdown
ADRs. Substantial changes to config shape, storage layout, plugin boundaries,
agent runtime behavior, or compatibility policy should either add a new ADR or
supersede an existing one.

## Consequences

Implementation work can proceed in smaller PRs while sharing one stable record
of the intended end state. Old compatibility behavior can be removed deliberately
instead of being rediscovered through search results and stale docs.
