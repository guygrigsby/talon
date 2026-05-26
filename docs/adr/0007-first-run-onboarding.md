# 0007 First-Run Onboarding (BOOTSTRAP)

Status: Accepted

Date: 2026-05-26

## Context

A fresh talon install ships no persona files. The main agent boots with no
identity, so it hallucinates one ("I'm GPT-4, no name"). Static scaffolding now
seeds default `IDENTITY/SOUL/AGENTS/USER` (ADR-adjacent, see
`internal/agentcontext`), but the defaults are placeholders the agent never
fills in.

openclaw solved this with a `BOOTSTRAP.md` sentinel: on the first conversation
the agent read it, interviewed the user to fill in identity, then deleted the
file. Its absence marked the workspace as initialized. talon referenced
`BOOTSTRAP.md` only in comments; the state machine was never ported and was
removed with openclaw compat (ADR 0003).

## Decision

Reintroduce a first-run onboarding flow owned by talon.

**Sentinel.** When the main agent's workspace is *fresh* (none of the four
persona files exist), gateway startup seeds the defaults AND writes
`BOOTSTRAP.md`. The sentinel is written only on the genuinely-first scaffold,
never recreated once removed, so onboarding runs exactly once and survives
restarts until completed.

**Trigger.** While `BOOTSTRAP.md` is present, its guidance is folded into the
system prompt as a leading, high-priority onboarding directive (ahead of persona
and configured prompt). The directive tells the agent to introduce itself,
interview the user conversationally, and call the `finish_onboarding` tool.

**Commit mechanism — dedicated tool, not filesystem tools.** A fresh install's
main agent has no read/write/edit tools (the agentcore path only attaches them
when a workspace is configured). So onboarding uses a purpose-built
`finish_onboarding` tool that takes structured fields (agent name, vibe, emoji,
user details), writes `IDENTITY.md` + `USER.md`, and deletes `BOOTSTRAP.md`. It
is deterministic, testable, and doesn't depend on granting the new agent raw
filesystem access.

**Scope.** Onboarding is gated purely by `BOOTSTRAP.md` presence in the persona
dir, and the sentinel is only ever seeded in the main workspace, so subagents
never onboard. `SOUL.md` (behavioral persona) keeps its default; the interview
fills only identity + user facts.

### Layering

- `internal/agentcontext` (pure, no agentcore dep) owns the lifecycle:
  `IsFresh`, `EnsureBootstrap`, `BootstrapActive`, `BootstrapPrompt`, and
  `ApplyOnboarding` (writes identity/user, removes the sentinel).
- `internal/agentcore_chat` wraps `ApplyOnboarding` in the `finish_onboarding`
  `agentcore.Tool`, attaches it during `BuildAgent` when onboarding is active,
  and prepends the directive in `buildSystemPrompt`.
- `cmd/talon/gateway.go` arms the sentinel on a fresh main workspace at startup.

## Consequences

- New installs run a short identity interview on first chat, then persist it.
- Deleting all persona files re-arms onboarding (intentional reset path).
- The legacy OpenAI/Anthropic chat path is not wired for onboarding; it is being
  removed (see `docs/migration-agentcore.md`). agentcore is the supported path.
- Future work: richer SOUL editing during onboarding, and a re-onboard command.
