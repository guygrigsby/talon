#!/usr/bin/env bash
# Backend dev loop: build the talon gateway, run it, then rebuild + bounce on
# changes under cmd/ or internal/. macOS-portable; no fswatch/entr needed —
# polls mtimes every 2 seconds. Logs to /tmp/talon-dev-gateway.log.
#
# This watches GO SOURCE ONLY. The UI is served by `vite dev` (make web-dev),
# which already hot-reloads on its own, so we deliberately do NOT watch web/ —
# and we run the gateway WITHOUT --web, leaving the UI to Vite on :5173. Run
# the two side-by-side (or use `make dev`, which launches both).
#
# Flags / env:
#   --port N            gateway port (default 18789; must match vite.config.ts
#                       proxy target). DEV_WATCH_PORT=N does the same.
#   --                  everything after is passed through to `gateway run`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG="/tmp/talon-dev-gateway.log"
PID_FILE="/tmp/talon-dev-gateway.pid"
WATCH_LOCK="/tmp/talon-dev-watch.lock"

PORT="${DEV_WATCH_PORT:-18789}"
PASSTHROUGH=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift ;;
    --)     shift; PASSTHROUGH=("$@"); break ;;
    *)      echo "dev-watch: unknown flag $1 (use -- to pass args to gateway run)" >&2; exit 2 ;;
  esac
  shift
done

# stop() kills our own child by PID before each restart, so a plain
# `gateway run` bounces cleanly without --force (which is also an unwired stub
# today). Pass any extra flags after `--`.
ARGS=(gateway run --port "$PORT")
if [[ ${#PASSTHROUGH[@]} -gt 0 ]]; then
  ARGS+=("${PASSTHROUGH[@]}")
fi

ts() { date "+%H:%M:%S"; }
log() { printf '[%s] %s\n' "$(ts)" "$*" | tee -a "$LOG" >&2; }

build() {
  log "build…"
  if (cd "$ROOT" && make build) >>"$LOG" 2>&1; then
    log "build ok"
    return 0
  fi
  log "BUILD FAILED — leaving previous binary running"
  return 1
}

stop() {
  if [[ -f "$PID_FILE" ]]; then
    local pid
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in 1 2 3 4 5 6; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.25
      done
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$PID_FILE"
  fi
}

start() {
  # exec inside the subshell so the subshell *becomes* the gateway — $! is
  # then the gateway PID itself, not a parent shell that would orphan it when
  # killed. Without exec, stop() would kill the subshell, leaving the gateway
  # holding the port and breaking the next start.
  (cd "$ROOT" && exec ./bin/talon "${ARGS[@]}" >>"$LOG" 2>&1) &
  local pid=$!
  echo "$pid" >"$PID_FILE"
  log "gateway started pid $pid (port $PORT)"
  # The gateway exits within ~600ms when config.toml is malformed or the port
  # can't be bound. Surface that instead of silently disappearing.
  sleep 0.6
  if ! kill -0 "$pid" 2>/dev/null; then
    log "gateway exited immediately — last log lines:"
    tail -8 "$LOG" | sed 's/^/    /' | tee -a "$LOG" >&2
    return 1
  fi
}

# Single-instance guard. A second watcher would race the first on the PID file
# (both writing it, both killing whatever the other launched) — a double-bounce
# cycle that produces a "could not connect" storm in the browser.
if [[ -f "$WATCH_LOCK" ]]; then
  EXISTING="$(cat "$WATCH_LOCK" 2>/dev/null || true)"
  if [[ -n "$EXISTING" ]] && kill -0 "$EXISTING" 2>/dev/null; then
    echo "dev-watch already running (pid $EXISTING); aborting." >&2
    exit 1
  fi
fi
echo $$ >"$WATCH_LOCK"
trap 'log "shutting down"; stop; rm -f "$WATCH_LOCK"; exit 0' INT TERM EXIT

: >"$LOG"
log "dev-watch starting; logs at $LOG"
log "pair with: make web-dev  (vite UI on :5173, proxies /ws to :$PORT)"
build || exit 1
start

# Polled mtime check over Go source only. bin/ (build output) and web/ (Vite's
# turf) are intentionally excluded so neither rebuild nor frontend edits create
# a feedback loop.
sources() {
  find "$ROOT/cmd" "$ROOT/internal" \
    -type f -name '*.go' \
    -not -path '*/node_modules/*' \
    -exec stat -f '%m %N' {} + 2>/dev/null | sort
  stat -f '%m %N' "$ROOT/go.mod" "$ROOT/go.sum" 2>/dev/null || true
}

LAST="$(sources | shasum | awk '{print $1}')"
log "watching cmd/ + internal/ + go.mod (poll every 2s)"

while :; do
  sleep 2
  NEW="$(sources | shasum | awk '{print $1}')"
  if [[ "$NEW" != "$LAST" ]]; then
    LAST="$NEW"
    log "change detected"
    sleep 0.4   # debounce: catch save-multiple-files in one rebuild
    LAST="$(sources | shasum | awk '{print $1}')"
    stop
    if build; then
      start
    fi
  fi
done
