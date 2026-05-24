# 2026-05-23: web frontend scaffold

Status: draft for review

## Goal

Stand up a first-party web frontend for talon at `web/`. This is the
scaffold-only milestone: project boots, dev server runs, prod build embeds
into the binary. Actual chat and admin views land in follow-up beads issues.

Long-term role: single SPA that serves both a chat UI (against the talon
gateway WebSocket) and an admin dashboard (health, channels, agents, models,
secrets, config). Replaces talon's dependence on the sibling `../openclaw/ui`
repo for everyday use.

## Constraints

- Talon's gateway HTTP server already accepts a `--web <dir>` flag that
  static-serves a directory (`gateway-run-with-ui` in the Makefile). The new
  scaffold preserves that override and adds `go:embed`-backed assets as the
  default when `--web` is unset.
- Single-binary distribution is a project value. Prod build must not require
  a sidecar.
- `talon dashboard` already hands off via a URL fragment containing the
  auto-auth token (see `PARITY.md` row for `dashboard`). The frontend will
  consume that same convention when auth lands; nothing to wire now.
- Project rule: no `~/.openclaw` writes. Frontend stays read-only against
  the gateway; any writes go through gateway RPCs.

## Stack

- **Framework:** SvelteKit (file-based routing across chat + admin views).
- **Build tool:** Vite (default for SvelteKit).
- **Language:** TypeScript.
- **Adapter:** `@sveltejs/adapter-static` in SPA mode
  (`fallback: 'index.html'`) so the Go static handler can serve client-side
  routes without a Node server.
- **Package manager:** pnpm. Matches the Svelte ecosystem default; the
  Makefile keeps `$(NPM)` overridable for anyone preferring npm.

## Layout

```
web/
  package.json
  pnpm-lock.yaml
  svelte.config.js          # adapter-static, fallback: 'index.html'
  vite.config.ts            # WS + RPC proxy to ws://127.0.0.1:18789
  tsconfig.json
  .gitignore                # node_modules, build, .svelte-kit
  src/
    app.html
    routes/
      +layout.svelte        # shell, nav between / and /admin
      +page.svelte          # chat landing placeholder
      admin/+page.svelte    # admin home placeholder
    lib/
      gateway/client.ts     # WS client stub (no implementation yet)
  static/                   # favicon etc.
  build/                    # produced by `pnpm build`; gitignored
  embed.go                  # package web, //go:embed all:build
```

`web/embed.go` lives next to the SvelteKit project so `//go:embed all:build`
resolves cleanly. The package is named `web` and exports a single
`embed.FS`:

```go
package web

import "embed"

//go:embed all:build
var Assets embed.FS
```

A `web/build/.gitkeep` placeholder ships in-tree so `go build` works before
the first `pnpm build`. The embedded FS will be near-empty in that case, and
the gateway handler treats a missing `index.html` as "UI not built, fall
back to 404 with a hint."

## Dev / prod paths

| Path | How | Source |
|---|---|---|
| Dev | `make web-dev` runs `pnpm --dir web dev` at `:5173`. Vite proxies `/ws` (and any RPC paths) to `ws://127.0.0.1:18789`. Hot reload, no Go rebuild. | Vite dev server |
| Prod (embedded) | `make build` depends on `make web-build`. `pnpm build` writes `web/build/`; Go embeds via `web/embed.go`. Single binary, single port. | `go:embed` |
| Prod (override) | Operators can still pass `--web <dir>` to `talon gateway run` to serve an out-of-tree build (existing flag). | Existing static handler |

## Gateway wiring

The embedded gateway server (see `internal/server`) already has an HTTP
handler that serves a `--web` directory. The change is to make the default
(no `--web` flag) serve the embedded assets:

1. Import `github.com/guygrigsby/talon/web` from the gateway package.
2. When `--web` is unset, mount `http.FileServer(http.FS(web.Assets))` on
   the same mux at `/`, with a SPA fallback that rewrites unknown paths to
   `/index.html` (so SvelteKit client routing works).
3. WebSocket handler precedence is unchanged: WS routes match first; the
   static handler is a catch-all.

The actual mount lives in whatever file currently parses `--web` (search
target during plan: `internal/server` for the `--web` flag handling).

## Makefile changes

The existing `WEB_DIR` / `WEB_DIST` defaults point at `../openclaw/ui`. The
scaffold repoints them at `web/`:

```make
WEB_DIR  ?= web
WEB_DIST ?= web/build
```

Existing targets keep working with the new defaults:

- `make web-install` → `pnpm --dir web install`
- `make web-dev` → `pnpm --dir web dev`
- `make web-build` → `pnpm --dir web build`
- `make web` → alias for `web-build`
- `make build` gains a dep on `web-build` (so embedded assets are fresh)
- `make all` keeps the existing meaning (build + web-build)
- `gateway-run-with-ui` still works against the new layout

`NPM ?= npm` becomes `PNPM ?= pnpm` with the `web-*` targets switched to
`$(PNPM)`. `NPM` is left intact for any leftover users.

## Initial scope (this change only)

1. SvelteKit + adapter-static project scaffolded in `web/` via the
   SvelteKit create CLI (`pnpm dlx sv create web`), selecting the
   minimal template, TypeScript syntax, and no add-ons.
2. `svelte.config.js` switched to adapter-static with SPA fallback.
3. `vite.config.ts` configured with a `/ws` proxy to
   `ws://127.0.0.1:18789` (placeholder for future use).
4. Two placeholder routes: `/` (chat landing) and `/admin` (admin home).
5. `web/embed.go` with `//go:embed all:build` and a `web/build/.gitkeep`.
6. `web/.gitignore` for `node_modules`, `build`, `.svelte-kit`.
7. Gateway static handler defaults to embedded assets when `--web` is unset.
8. Makefile `WEB_DIR`/`WEB_DIST` repointed at `web/`; `make build` gains
   the `web-build` dep; targets switched to `$(PNPM)`.

## Explicitly NOT in this change

- Gateway WebSocket client implementation (framing, reconnect, auth).
- Real chat UI; real admin views; agent/model/secrets/config screens.
- Auth token handoff (the `talon dashboard` URL-fragment flow).
- CI updates (pnpm install step in workflows).
- Cross-build target changes (`make cross` stays Go-only for now).
- Removing the legacy `../openclaw/ui` references from docs.

## Testing

- `make build` succeeds with an unbuilt `web/build/` (only the `.gitkeep`
  file present).
- `make web-build && make build` produces a binary that serves
  `index.html` at `/` and a 200 for `/admin` (SPA fallback).
- `make web-dev` boots Vite at `:5173` with no errors.
- `go vet ./...` and `go test ./...` stay green.

## Follow-up beads issues to file

IDs assigned via `bd create` after this scaffold lands.

- Gateway WebSocket client + reconnect/auth (token from URL fragment per
  `talon dashboard` convention).
- Chat view (initial render of streamed messages).
- Admin home: health + channel list.
- CI: pnpm install + `pnpm --dir web build` in the build workflow.
- Remove `../openclaw/ui` references from `PARITY.md` and other docs once
  parity is reached.
