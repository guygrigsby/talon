# Talon CLI configuration audit + missing secret-entry commands

Status: Ready to implement (handoff)
Date: 2026-05-26

> Executable implementation plan. Another instance should pick this up and build
> it end to end (code + tests + verification). File the follow-ups as beads
> issues during execution.

## Context

ADR 0006 banned plaintext secrets from config: `config.Set` rejects any
non-reference value at a sensitive key (`assertNoPlaintextSecretWrite`,
`internal/config/edit.go:202`). The only talon-owned flow that stores a secret
and writes its reference is the two channel wizards (Telegram, BlueBubbles),
which hardcode `secrets.StoreKeychainSecret` → `config.Set`.

**Audit result — what's blocked.** Every non-secret knob is reachable via
`talon config set <path> <value>`. The blocked features are secret-bearing ones
with no guided entry; a fresh user can't configure them without manually running
`security`/`op` and hand-writing refs:

| Blocked feature | Secret path (merged view) | Today |
|---|---|---|
| LLM provider API keys | `plugins.entries.anthropic.config.apiKey`; `plugins.entries.openai-compat.config.providers.<id>.apiKey` | no command → **chat unusable on fresh install** |
| Gateway auth | `gateway.auth.token` / `gateway.auth.password` (+ `gateway.auth.mode`) | no command |
| Brave web search | `plugins.entries.brave.config.webSearch.apiKey` | no command |

Out of scope (reachable via `config set`, or unrelated): non-secret knobs
(`agent.system_prompt`, `memory.enabled`, channel advanced fields); service-mgmt
/discovery stubs (`talon-4an`, `talon-r0p`).

**Decisions:** build the full set — keystone `secrets set` command + `configure
provider` + `configure gateway` wizards. The secret-store backend is a
**configured preference** chosen in `configure gateway`. Backends: macOS keychain
and 1Password. **talon WRITES into the chosen backend** (keychain via `security`,
1Password via `op item create`) — no paste-the-ref. The Telegram and BlueBubbles
wizards must be migrated onto the same backend-aware store path. A "manual/paste"
escape hatch remains for unsupported stores.

## Design

### A. Backend-aware store primitive — `internal/secrets`
- Add `StoreSecret(ctx, backend, target, secret string) (ref string, err error)` dispatching on backend:
  - `keychain` → existing `StoreKeychainSecret` (macOS) → `keychain://…`.
  - `op` → 1Password **write** (see B) → `op://<vault>/<title>/password`.
- Keeps op isolation per ADR 0006 (op stays behind `talon-op-plugin`).

### B. 1Password write support — `apps/talon-op-plugin`
- Add a store mode to `talon-op-plugin` (it already bootstraps `OP_SERVICE_ACCOUNT_TOKEN` from keychain `talon.opAccessToken`; reuse it). Reads the secret from **stdin** (never argv), runs `op item create --category=password --vault=<vault> --title=<title> password=-`, prints the `op://<vault>/<title>/password` ref to stdout.
- `internal/secrets` invokes the plugin in store mode for the `op` backend.
- Requires the user signed into `op` CLI / a service-account token; surface a clear error otherwise.

### C. Shared CLI helper — `cmd/talon/secret_store.go` (new)
- `var storeSecret = secrets.StoreSecret` — injectable so wizard/command tests use a fake (existing wizards call the package func directly and are untested; this fixes that).
- `resolveSecretStore(merged) (backend, vault string)` — reads `secrets.store` (`keychain`|`op`|`manual`) + `secrets.opVault`; default `darwin`→`keychain`, else→`op`.
- `acquireSecretRef(ctx, in, out, backend, vault, keychainTarget, opTitle string) (ref, err)`:
  - `keychain`/`op`: no-echo prompt for the raw secret → `storeSecret(...)` → ref.
  - `manual`: prompt for an existing `op://`/`keychain://` ref → validate `secrets.IsReference`.
  - Verify before returning: `secrets.NewResolver().Resolve(ctx, ref)`, discard value (never print), error if unresolvable.
  - No-echo input via `golang.org/x/term` (**new dep**; BSD, Go-team). Line-read fallback on non-TTY.

### D. `talon secrets set <config-path>` — `cmd/talon/secrets.go`
- Flags: `--from-env VAR`, `--stdin`, `--op <ref>`/`--ref <ref>` (passthrough); default = no-echo prompt.
- `config.ParsePath` → backend via `resolveSecretStore` → `keychainTarget="talon."+config.SegPath(segments)`, `opTitle="talon/"+config.SegPath(segments)` → `acquireSecretRef` → `config.Set(SetReplaceSafe)` → `emitReloadHint` (reuse `cmd/talon/models_subs.go:314`). Never echo secret/resolved value.
- Unblocks gateway auth, Brave, any future secret in one command.

### E. `talon configure provider` — `cmd/talon/configure_provider.go` (new)
- Register `{Kind:"provider", Name:"provider", Label:"LLM provider (API key)", Run: configureProvider}` in `configureWizardsForTest` (`configure.go:101`); add `configureProviderCmd` mirroring `configureChannelCmd`.
- Pick provider (anthropic, openai, deepseek, mistral, groq, openrouter, custom). Writes:
  - anthropic → `plugins.entries.anthropic.config.apiKey` = ref, `plugins.entries.anthropic.enabled` = true.
  - openai-compat family → `plugins.entries.openai-compat.config.providers.<id>.{apiKey: ref, baseUrl}`, `plugins.entries.openai-compat.enabled` = true. Default `baseUrl` per provider; `custom` prompts.
- Key via `acquireSecretRef` honoring the configured backend; `keychainTarget="talon.providers.<id>.apiKey"`.
- If `agents.defaults.model.primary` unset, offer to set it. Restart hint.

### F. `talon configure gateway` — `cmd/talon/configure_gateway.go` (new)
- Register `{Kind:"gateway", ...}`; add `configureGatewayCmd`.
- Step 1: choose secret-store backend — keychain (macOS only), 1Password, or manual → write `secrets.store`; if `op`, prompt + write `secrets.opVault`. Pass the chosen backend directly to `acquireSecretRef`.
- Step 2: auth mode none/token/password → if secret, `acquireSecretRef` → write `gateway.auth.token`|`gateway.auth.password` + `gateway.auth.mode` (merged-view nested paths; `pruneInactiveGatewayAuth`, `edit.go:479`, manages these). Restart hint.

### G. Migrate existing wizards — `cmd/talon/configure.go`
- Replace the hardcoded `secrets.StoreKeychainSecret(...)` calls in `configureTelegram` (`configure.go:376`) and `configureBluebubbles` (`configure.go:523`) with `acquireSecretRef`/`storeSecret` honoring `secrets.store`. Telegram/BlueBubbles then store in keychain OR 1Password per preference.

### Notes
- `secrets.store`, `secrets.opVault`, `channels.*`, `gateway.auth.*` are overlay/merged-view keys; `config.Set` persists arbitrary dotted paths (Telegram is already `mapstructure:"-"`, overlay-only) → **no `internal/talonconfig/native.go` change needed**.
- Reuse: `secrets.{StoreKeychainSecret,IsReference,NewResolver}`, `config.{ParsePath,Set,ClassifyReload,SegPath}`, `emitReloadHint`, the `configureWizard`/`findWizard`/`runWizardSubmenu` machinery, `patchWizards` (`configure_menu_test.go:22`).

## Tests
Mirror `configure_*_test.go` (drive `Run(in,out)` with scripted stdin; patch `storeSecret` to a fake; temp `TALON_STATE_DIR`).
- `secret_store_test.go`: `resolveSecretStore` default-by-platform + override; `acquireSecretRef` keychain + op (fake storer) + manual (valid ref accepted, plaintext rejected); verify-resolves failure path.
- `secrets_set_test.go`: `--ref` passthrough; `--from-env` writes a **ref not plaintext**; sensitive-key plaintext never persisted.
- `configure_provider_test.go`: anthropic + an openai-compat provider write correct `plugins.entries.*` + `enabled`; key path gets a ref.
- `configure_gateway_test.go`: writes `secrets.store`, `secrets.opVault` (op), `gateway.auth.mode`, `gateway.auth.token` ref.
- `configure_telegram`/`bluebubbles`: full-wizard tests now possible via injected `storeSecret`; assert ref (keychain and op backends) written, not plaintext.
- `talon-op-plugin` store mode: secret read from stdin, `op item create` invoked with expected args (fake/`op` shim), ref returned.

## Verification
```bash
make build && go test ./cmd/talon/... ./internal/config/... ./internal/secrets/... ./apps/talon-op-plugin/... && make vet
```
Manual e2e (macOS): `talon configure gateway` (keychain+token) → `talon secrets audit` shows `gateway.auth.token` as `ref`; switch `secrets.store=op` + vault, `talon configure provider` (anthropic) → audit shows apiKey as `op://` ref and the item exists in 1Password; `talon configure channel telegram` stores token in the selected backend; gateway boots and resolves; no plaintext in `config.toml`, no secret in stdout. Linux: keychain hidden; op write path works with `op` signed in.

## Follow-ups (file as beads issues during execution)
- Brave/web-search convenience wizard (keystone already covers it).
- `secrets rotate`/`unset` symmetry for removing a stored secret + its ref.
