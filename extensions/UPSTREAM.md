# extensions/ — vendored openclaw default plugins

This directory is a **vendored copy** of openclaw's `dist/extensions/`,
brought in so talon ships drop-in compatible with the same plugin set
openclaw users are accustomed to.

## Source

- Upstream repo: <https://github.com/openclaw/openclaw>
- Vendored at: commit `1fac98ab16f126574395ecf643e2688c67f88218`
  (`feat(ui): repurpose Control UI for talon backend`)
- Date: 2026-04-30

## What's included

Everything under openclaw's `dist/extensions/` except:

- `node_modules/` (any nested copies — installed at runtime by the
  shim subprocess if a plugin needs them)
- `.openclaw-install-stage*` / `.openclaw-runtime-deps-*` (transient
  install artifacts produced by openclaw's runtime dep bootstrapper)
- `qa-channel/`, `qa-lab/`, `qa-matrix/` (test-only fixtures upstream
  excludes from its npm package via the same `package.json` `files`
  exclusion list)

## Why dist/, not src/

openclaw extensions are authored in TypeScript and compiled to JS in
`dist/`. Talon's `apps/openclaw-plugin-host` shim is a Node subprocess
that loads JS modules directly via `import()` — it doesn't carry a
TypeScript toolchain. Vendoring the compiled artifacts means the shim
loads them with no extra build step, matching what end-users get when
they `npm install openclaw`.

The cost: diffs in this directory are noisy (compilation output) and
not always meaningful in isolation. When reviewing upstream syncs,
look at the corresponding source change on the upstream side instead.

## Pulling upstream changes

```bash
# Bring openclaw clone up to date first.
cd ../openclaw && git fetch && git checkout main && git pull

# In talon: dry-run the sync to see what would change.
cd ../talon && scripts/sync-extensions.sh --dry-run

# Apply the sync, review, commit.
scripts/sync-extensions.sh
git diff extensions/   # inspect the full diff before staging
git add extensions/ extensions/UPSTREAM.md
```

The sync script updates `extensions/UPSTREAM.md` with the new commit
SHA so the lineage is recoverable from any point in talon's history.

## Auto-load and config

This directory is the *source of truth* for the bundled plugin
binaries. Wiring them into talon's runtime — auto-discovery, default
`plugins.entries.*` config — is a separate concern and tracked
separately from this vendoring task.

## Why not git subtree

Considered. Rejected because openclaw's git history is ~36k commits
and 600 MB; importing it (even a `--squash`) into talon's history
brings noise that doesn't pay for itself. The sync-script flow keeps
talon's history clean and lets upstream pulls land as a single
commit per merge with a clear summary.

## Why not an npm dependency

Considered too — `npm install openclaw` would give us
`node_modules/openclaw/dist/extensions/` for free. Rejected because
talon's "drop-in replacement" promise reads better when the plugins
are visibly bundled in the talon repo than when they're hidden in a
transitive dependency. Also: pinning a single npm version doesn't
mesh with the per-extension cherry-pick workflow we want.
