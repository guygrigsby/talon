# 0004 File-Backed Subagents

Status: Accepted

Date: 2026-05-26

## Context

The previous agent model treated subagents like separate personas with separate
workspaces and identity files. Talon now has one main chat agent. Subagents are
task-specific skills the main agent can delegate to, normally with a model that
fits the task.

## Decision

Define subagents as Markdown files under `~/.talon/subagents/*.md` using an
opencode-style front matter block:

```markdown
---
description: Reviews code for regressions.
model: anthropic/claude-sonnet-4-6
tools: [read, grep, edit]
---
Prompt instructions for the subagent.
```

The file name is the default subagent id. Front matter may override `id`,
`name`, `description`, `model`, `tools`, and `disabled`. The main agent keeps
its current `IDENTITY.md`, `SOUL.md`, and related workspace Markdown files.
Subagents inherit the main/default workspace rather than owning separate
workspace identity state.

## Consequences

`agents.list` merges the main config-backed agent with file-backed subagents.
The `agents` and `subagent` tools expose available subagents to the main model,
including ids and descriptions, so the model can delegate by exact id.

Subagent defaults are edited by changing the subagent file front matter. The
TOML config no longer has a `[[subagents]]` section.
