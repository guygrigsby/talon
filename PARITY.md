# talon ↔ openclaw command parity

Source-of-truth tracking for `talon-01s` (drop-in alias). Generated against
`openclaw 2026.4.24 (cbcfdf6)`. Subcommand counts come from `openclaw <cmd>
--help`. Status legend: ✓ shipped · ◐ partial · ✗ missing · ⊘ won't implement.

## Top-level commands

| Command | Status | Blocking issues / notes |
|---|---|---|
| `acp` (`client`) | ✗ | talon-aws (gateway server), ACP runtime |
| `agent` | ✗ | talon-aws, talon-ekd, talon-98f |
| `agents` (7 subs) | ◐ `list` + `bindings` | `list` renders a tab-aligned table (default agent first, then alpha; columns: ID, MODEL, WORKSPACE, FALLBACKS, NAME); use `--json` for raw RPC payload (talon-8eb). `bindings` lists `channels.<id>.agentId` entries (with optional `--agent <id>` filter). Other subs (`bind`/`unbind`/`add`/`set-identity`/`delete`): talon-48e, talon-aws |
| `approvals` (4 subs) | ✗ | talon-97i, talon-aws |
| `backup` (3 subs) | ✗ | talon-aws (state on disk to back up) |
| `capability` (10 subs) | ✗ | talon-ekd, talon-aws |
| `channels` (10 subs) | ✗ | talon-kqk, talon-aws |
| `chat` | ◐ wrong semantics | talon-dcj — openclaw `chat` = `tui --local`; talon `chat` is one-shot stream. Plus talon-rpk, talon-yct, talon-z8h, talon-8h2 |
| `clawbot` (1 sub: `qr`) | ⊘ | legacy aliases, not worth cloning |
| `completion` | ✓ | Cobra built-in (`completion bash|zsh|fish|powershell`) |
| `config` (6 subs) | ◐ | Layered overlay model (talon-2sv): read merges `~/.talon` over `~/.openclaw`, writes target `~/.talon` only. talon-1oj (set rich modes — Phase B), talon-7vk (whitespace), talon-9ic (tombstones for openclaw-layer deletes) |
| `configure` (interactive) | ✗ | depends on entire config surface; large port |
| `cron` (10 subs) | ◐ list/add/remove/rm/run/status/show/enable/disable/runs | gateway implements `cron.*` RPCs (talon-8z0). CLI wraps as `talon cron list/add/remove/run/status/show/enable/disable/runs` (talon-3tg). `remove` aliases `rm` to match openclaw. `list` filters to enabled jobs by default; `--all` includes disabled. `status` returns scheduler metadata (running, jobCount, enabledCount, nextRunMs). `show <id>` returns a single job. `enable`/`disable` toggle the enabled flag. Persistence at `~/.talon/cron/jobs.json` + run log at `~/.talon/cron/runs.jsonl`. Standard 5/6-field cron expressions + descriptors (@hourly etc.). Out-of-scope for v1: per-job timezones, jitter, `cron edit` (use remove+add), isolated-agent sessions. |
| `daemon` (6 subs) | ⊘ | legacy alias for `gateway` service mgmt |
| `dashboard` | ✓ | Prints + clipboards + opens the gateway URL with token auto-auth in fragment. Token resolved through the secrets resolver (op:// / keychain:// references work). |
| `devices` (8 subs) | ✗ | talon-job, talon-26v, talon-xk1, talon-aws |
| `directory` (3 subs) | ✗ | talon-kqk, talon-aws |
| `dns` (1 sub: `setup`) | ✗ | wide-area discovery; depends on bonjour work (talon-r0p) |
| `docs` | ◐ | URL + search-link surface (no MCP shell-out yet — that's the openclaw runtime dep we'd rather not take on). Args render `https://docs.openclaw.ai/?q=<query>`. |
| `doctor` | ✗ | talon-uyp |
| `exec-policy` (3 subs) | ✗ | talon-aws (host approvals integration) |
| `gateway` (14 subs) | ◐ 6/14 | see below |
| `health` | ✓ | RPC `health` (was bug `health.get`, fixed in talon-a3h) |
| `help` | ✓ | Cobra default |
| `hooks` (7 subs) | ✗ | talon-aws, hook runtime |
| `infer` (10 subs) | ✗ | aliased to `capability` in openclaw — same blockers |
| `logs` | ✗ | talon-set, talon-aws |
| `mcp` (6 subs) | ✗ | talon-v8k |
| `memory` | ✗ | depends on memory backend |
| `message` (24 subs) | ✗ | talon-kqk, talon-aws — large surface |
| `models` (9 subs) | ◐ list/set/fallbacks/aliases | `models` (and `models list`) renders a tab-aligned table (sorted by ID; columns: ID, MODALITIES, CTX, REASONING, ALIAS, NAME); use `--json` for raw RPC payload. `models set <model>` writes `agents.defaults.model.primary`. `models fallbacks list/add/remove/clear` manages `agents.defaults.model.fallbacks`. `models aliases list/add/remove` manages `agents.defaults.models.<id>.alias`. All write subs emit per-path reload hints. Missing: `status --probe`, `scan`, `auth *`, `set-image` / `image-fallbacks` (talon-d0v). |
| `node` (6 subs) | ✗ | talon-yw5, talon-aws |
| `nodes` (15 subs) | ✗ | talon-yw5, talon-aws |
| `onboard` (interactive) | ✗ | depends on entire setup surface |
| `pairing` (3 subs) | ✗ | talon-xk1, talon-26v |
| `plugins` (10 subs) | ✗ | talon-aub, talon-yw5 |
| `proxy` (8 subs) | ✗ | proxy runtime |
| `qr` | ✗ | depends on talon-xk1 (proper device pairing) |
| `reset` | ✗ | talon-aws (state to reset) |
| `sandbox` (3 subs) | ✗ | sandbox runtime |
| `secrets` (4 subs) | ◐ audit/migrate/keychain-bootstrap/reload | `talon secrets audit` (alias `ls`) audits the merged config (literal/ref/empty) with `--check` (exit non-zero on findings) and `--allow-exec` (parity flag). `migrate [--apply] [--filter <substr>]` (talon-yt0) is the bulk sweep — dry-run by default, walks merged config + auth-profiles.json across all agents, writes each literal to the macOS keychain as `keychain://talon.<dotted-path>`, rewrites refs in talon overlay (openclaw layer untouched). `keychain-bootstrap` stores the OP service-account token in the macOS keychain so `talon-op-plugin` auths non-interactively from a fresh shell. `reload` calls `secrets.reload` RPC to hot-swap the gateway's runtime snapshot. openclaw's `migrate <path> [--vault]` (per-secret 1Password destination) was dropped — the bulk keychain path covers the actual use case ("get plaintext off disk") without making users hunt for dotted paths. `apply` (apply a secrets plan from file) is not yet ported — talon-ekv. `configure` is interactive and out of scope. |
| `security` (1 sub: `audit`) | ✗ | talon-ekv (config audit) |
| `sessions` (1 sub: `cleanup`) | ✗ | talon-c0b, talon-8lr |
| `setup` | ✗ | talon-aws |
| `skills` (6 subs) | ✗ | talon-ced |
| `status` | ◐ | RPC `channels.status` works; `--all`/`--usage` flag rendering deferred |
| `system` (3 subs) | ✗ | talon-aws |
| `tasks` (7 subs) | ✗ | talon-aws |
| `terminal` | ✗ | alias for `tui --local` — talon-pcn |
| `tui` | ✗ | talon-pcn |
| `uninstall` | ✗ | talon-aws |
| `update` (2 subs) | ✗ | talon-87g (homebrew), talon-u9x (go install) |
| `version` | ✓ | Both `talon version` subcommand and `-V`/`--version` flag (parity with openclaw's commander surface). |
| `webhooks` (1 sub: `gmail`) | ✗ | depends on gogcli integration |

## `gateway` subcommands (12 total)

| Subcommand | Status | Notes |
|---|---|---|
| `call` | ✓ | RPC pass-through (talon-lhh) |
| `diagnostics export` | ✓ | Writes a sanitized .zip with manifest, redacted merged config, paths layout, optional health snapshot, audit-log tail. Best-effort health probe (continues on dial failure). |
| `discover` | ✗ | talon-r0p — needs Bonjour mDNS |
| `health` | ✓ | RPC `health` (talon-lhh) |
| `install` | ✗ | talon-4an → blocked on talon-aws |
| `probe` | ◐ | single-target only; openclaw probes localhost+remote+SSH-tunnel |
| `restart` | ✗ | talon-4an → talon-aws |
| `run` | ✗ | talon-aws |
| `stability` | ✓ | RPC `diagnostics.stability` (talon-lhh) |
| `start` | ✗ | talon-4an → talon-aws |
| `status` | ◐ | probe + `--deep` service scan; missing localhost+remote dual probe, SSH tunnel |
| `stop` | ✗ | talon-4an → talon-aws |
| `uninstall` | ✗ | talon-4an → talon-aws |
| `usage-cost` | ✓ | RPC `usage.cost` (talon-lhh) |

## `config` subcommands (6 total)

| Subcommand | Status | Notes |
|---|---|---|
| `file` | ✓ | |
| `get` | ✓ | |
| `schema` | ✓ | Prints cached schema by default; `--refresh` fetches via `config.schema` RPC and writes the cache used by `config validate`; `--section <name>` filters to one subschema (dotted to drill in, e.g. `gateway.auth`). |
| `set` | ◐ | Value mode with `--strict-json` (alias `--json`), `--dry-run`, `--merge`, `--replace`, openclaw bracket path syntax (`a[0]`, `a["k"]`), protected-path guard against the **merged** view, `gateway.auth.mode` credential pruning (talon overlay only) with stale-openclaw warning, backup rotation (`.bak`–`.bak.4`) and JSONL audit log on `~/.talon/logs/config-audit.jsonl` (talon-1xa, talon-2sv). Output is byte-identical to openclaw's `JSON.stringify(v, null, 2) + "\n"`; idempotent sets (same value) short-circuit before rotating backups or appending audit (talon-7vk). Missing: `--ref-provider`/`--batch-file`/provider builder modes — Phase B (talon-1oj); tombstones for openclaw-layer deletes (talon-9ic) |
| `unset` | ✓ | |
| `validate` | ✓ | Schema-aware via cached schema at `~/.talon/cache/config-schema.json` (`config schema --refresh` populates from gateway). `--strict` requires a usable cache; default falls back to syntax-only with a warning when the cache is missing or the schema fails to compile (talon-p4q). Note: openclaw's current schema has dangling `$defs` refs that fail compile — tracked upstream as talon-q4m. |

## Intentional divergence: post-write reload hints (talon-5zx)

openclaw's `config set` always prints "Restart the gateway to apply." talon
classifies each path and emits a class-aware hint instead:

- **next-rpc** (default): "applies on the gateway's next request — no restart needed"
- **hup**: "send SIGHUP to the running gateway to apply <path> (or restart)"
- **restart**: "restart the gateway to apply (<path> is consumed at startup)"

Restart-class paths today: `gateway.port`, `gateway.bind`, `gateway.auth.{mode,token,password}`, `gateway.tailscale.*`, `gateway.controlUi.*`, `plugins.entries.*.enabled`, `plugins.deny`, `plugins.load.paths`, `skills.*`. HUP class is empty until talon's embedded gateway gains a SIGHUP handler (talon-f06). Override per-call with `--reload=next-rpc|hup|restart`. Policy: talon never auto-restarts, never auto-watches.

## Cross-cutting flag gaps

openclaw's gateway-targeting subcommands share a `gatewayCallOpts` flag set
that talon doesn't honor today: `--url`, `--token`, `--password`,
`--timeout`, `--ssh`/`--ssh-identity`/`--ssh-auto`, `--json`. talon currently:

- Reads URL/token only from config (`--url`/`--token` honored on `gateway
  status`/`probe` but not `health`/`stability`/`usage-cost`/`call`).
- Has no password auth path (talon-ekv).
- Has no SSH tunnel path.
- Has `--json` as a global flag, but `emit()` always pretty-prints — see
  `cmd/talon/main.go` `|| true`.

## Output format gap

openclaw renders themed human output (channels summaries, stability tables,
health channel lines) via JS modules when `--json` isn't set. talon emits JSON
unconditionally for now. True parity requires porting those renderers — see
talon-8eb (pretty/columnar typed output) and talon-htt (history timeline).

## What can't be ported as-is

- **Interactive flows** (`configure`, `onboard`, `setup`, `update wizard`,
  `secrets configure`) — would need a Go interactive prompt library and a
  rewrite of openclaw's flows.
- **Legacy aliases** (`clawbot`, `daemon`) — explicitly legacy upstream;
  cloning adds churn.
- **Built-in Cobra equivalents** (`completion`, `help`) — use the framework's
  own implementations.
