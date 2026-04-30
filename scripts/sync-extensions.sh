#!/usr/bin/env bash
#
# sync-extensions.sh — pull the latest dist/extensions/ from a sibling
# openclaw checkout into talon's vendored extensions/ tree.
#
# Usage:
#   scripts/sync-extensions.sh              # apply changes
#   scripts/sync-extensions.sh --dry-run    # preview without writing
#   OPENCLAW_REPO=/path/to/clone scripts/sync-extensions.sh
#
# What it does:
#   1. Resolves the openclaw clone path (default ../openclaw).
#   2. Refuses to run if openclaw's working tree is dirty — vendoring a
#      half-built dist/ would reproduce non-committed local changes
#      into talon, which is exactly the bug review asymmetry we want
#      to avoid.
#   3. rsync's openclaw/dist/extensions/ → talon/extensions/, with the
#      same exclusion list openclaw's package.json `files` field uses
#      to keep its npm tarball lean.
#   4. Updates extensions/UPSTREAM.md with the new commit SHA, summary
#      line, and timestamp.
#
# Review checklist after running:
#   - `git diff extensions/UPSTREAM.md` — sanity-check SHA + date
#   - `git diff --stat extensions/` — surface size / scope of the sync
#   - For risky-looking changes, look at the upstream commit in
#     openclaw's repo: git -C ../openclaw show <sha>

set -euo pipefail

# Resolve repo root (this script lives in <repo>/scripts/).
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OPENCLAW_REPO="${OPENCLAW_REPO:-$REPO_ROOT/../openclaw}"
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --dry-run|-n) DRY_RUN=true ;;
    -h|--help)
      sed -n '2,30p' "$0"
      exit 0
      ;;
    *)
      echo "sync-extensions: unrecognized arg $arg" >&2
      exit 2
      ;;
  esac
done

if [[ ! -d "$OPENCLAW_REPO/.git" ]]; then
  echo "sync-extensions: openclaw clone not found at $OPENCLAW_REPO" >&2
  echo "  set OPENCLAW_REPO=/path/to/clone or symlink the repo there" >&2
  exit 1
fi

if [[ ! -d "$OPENCLAW_REPO/dist/extensions" ]]; then
  echo "sync-extensions: $OPENCLAW_REPO/dist/extensions does not exist" >&2
  echo "  build the upstream first: (cd $OPENCLAW_REPO && npm install && npm run build)" >&2
  exit 1
fi

# Refuse to vendor uncommitted upstream state. Half-built dist
# directories or local-only commits are almost never what we want
# to land in talon.
if [[ -n "$(git -C "$OPENCLAW_REPO" status --porcelain)" ]]; then
  echo "sync-extensions: $OPENCLAW_REPO has uncommitted changes — refusing to sync" >&2
  echo "  commit or stash them first; the vendored copy must come from a clean tree" >&2
  exit 1
fi

UPSTREAM_SHA="$(git -C "$OPENCLAW_REPO" rev-parse HEAD)"
UPSTREAM_SUMMARY="$(git -C "$OPENCLAW_REPO" log -1 --format='%s')"
TODAY="$(date -u +%Y-%m-%d)"

RSYNC_FLAGS=(-a --delete)
if $DRY_RUN; then
  RSYNC_FLAGS+=(--dry-run --itemize-changes)
fi

# Note: --delete makes the local extensions/ exactly mirror upstream's
# dist/extensions/ minus the exclusions. Without it, removed-upstream
# extensions would linger in talon. UPSTREAM.md is preserved by the
# explicit exclude below.
rsync "${RSYNC_FLAGS[@]}" \
  --exclude='UPSTREAM.md' \
  --exclude='node_modules' \
  --exclude='.openclaw-install-stage*' \
  --exclude='.openclaw-runtime-deps-*' \
  --exclude='.openclaw-runtime-deps-stamp.json' \
  --exclude='qa-channel' \
  --exclude='qa-lab' \
  --exclude='qa-matrix' \
  "$OPENCLAW_REPO/dist/extensions/" \
  "$REPO_ROOT/extensions/"

if $DRY_RUN; then
  echo "(dry run) would update UPSTREAM.md to $UPSTREAM_SHA / $TODAY"
  exit 0
fi

# Update the UPSTREAM.md vendoring stanza in place. We patch only the
# three lines (commit, summary, date) so any other edits to the file
# (workflow notes, etc.) are preserved.
UPSTREAM_FILE="$REPO_ROOT/extensions/UPSTREAM.md"
if [[ -f "$UPSTREAM_FILE" ]]; then
  python3 - "$UPSTREAM_FILE" "$UPSTREAM_SHA" "$UPSTREAM_SUMMARY" "$TODAY" <<'PY'
import re, sys
path, sha, summary, today = sys.argv[1:5]
with open(path, encoding="utf-8") as f:
    text = f.read()
text = re.sub(
    r"- Vendored at: commit `[0-9a-f]+`\s*\n\s*\(`[^`]*`\)",
    f"- Vendored at: commit `{sha}`\n  (`{summary}`)",
    text, count=1,
)
text = re.sub(r"- Date: \d{4}-\d{2}-\d{2}", f"- Date: {today}", text, count=1)
with open(path, "w", encoding="utf-8") as f:
    f.write(text)
PY
fi

echo "synced extensions/ from openclaw@$UPSTREAM_SHA"
echo "next: review with 'git diff extensions/' and commit"
