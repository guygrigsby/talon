# 0009 Agent Tool Access Policy

Status: Accepted

Date: 2026-05-26

## Context

Talon now treats the main chat agent as the coordinator. Subagents are
task-specific workers with their own prompt, model, and tool permissions. The
main agent previously received every assembled tool by default, which made it
hard to test unknown models and contradicted the coordinator-only direction.

The runtime still has two tool execution boundaries: the agentcore path and the
older direct-provider `ToolRunner` loop used by inline/channel paths. The old
runner is legacy code and should not grow into another policy system, but it
must enforce the same access decisions until those paths move fully to
agentcore.

## Decision

Add one shared tool access policy resolver. The default is compatible:
configured agents get all assembled tools unless a policy says otherwise.

Policy inputs:

- Config-backed agents may use `tools.enabled = false` to expose no tools.
- Config-backed agents may use `tools.allow` / `tools.allowed` to expose only
  named tools.
- Native TOML exposes the main-agent form as `[agent].tools_enabled` and
  `[agent].tools_allow`.
- File-backed subagents use opencode-style front matter:
  `tools: [read, grep, edit]`.

The policy filters both tool advertisement and tool execution. Agentcore applies
the filter when building the agent. The legacy direct-provider `ToolRunner`
path only wraps its existing runner with the same filter; no new behavior should
be added there beyond maintaining parity while it is retired.

`[agent].tools_allow` is main-agent policy, not subagent default policy.
Subagents should declare their own permissions in their Markdown front matter.
Runtime JSON may still understand `agents.defaults.tools` for migration and
compatibility during the architecture transition.

Passive memory context is not considered tool access. Disabling tools prevents
the model from calling memory tools such as `remember` and `recall`, but does
not remove non-callable context injection.

## Consequences

The main agent can now be configured as a coordinator by allowing only
delegation-oriented tools, or as chat-only with tools disabled. Unknown model
testing can start from no tools or a read-only allowlist before granting broader
capabilities.

`agents.list` reports the resolved policy so the UI can show `default`,
explicit tool names, or `none`. This keeps the visible agent inventory aligned
with what the runtime will advertise to the model.

The legacy direct-provider runner remains a removal target. New tool policy
features should be added to the shared resolver and agentcore path first, with
only minimal compatibility enforcement in the old runner until channel and
inline dispatch no longer depend on it.
