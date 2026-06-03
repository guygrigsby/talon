BINARY := talon
PKG    := ./cmd/talon
BIN    := bin/$(BINARY)

LDFLAGS := -s -w

GO   ?= go
NPM  ?= npm
PNPM ?= pnpm

# First-party talon web frontend (SvelteKit + Vite). Override to point at
# a different build.
WEB_DIR  ?= web
WEB_DIST ?= web/build

GO_SRC := $(shell find cmd internal web -name '*.go' 2>/dev/null)

# First-party Go plugins (deepseek, telegram, brave, whisper,
# bluebubbles, mac-notify) ship inside the talon binary and run via
# `talon plugin run <name>` — no per-plugin binary to build. PLUGINS
# only lists the standalone helper CLIs that exist as independent
# processes. Currently just op (1Password CLI wrapper) — the keychain
# resolver was inlined into the talon binary so one-binary installs
# can resolve keychain:// refs without a sidecar.
PLUGINS := op
PLUGIN_BINS := $(addprefix bin/talon-,$(addsuffix -plugin,$(PLUGINS)))

.PHONY: build build-with-ui all install run dev dev-backend dev-open gateway-run gateway-run-with-ui redeploy plugins test test-e2e bench vet fmt tidy clean cross web web-install web-dev web-build web-check web-test web-test-install docker-build docker-run docker-stop docker-bounce docker-logs proto proto-tools smoke

build: $(BIN) plugins

# build-with-ui builds the SvelteKit UI and then force-rebuilds the binary so
# the freshly built assets are embedded. go:embed only picks up changes when
# the Go compiler runs, and $(BIN)'s prerequisites are .go-only on purpose
# (plain `build` stays node-free for headless / CLI-only installs), so a bare
# `make build` after `web-build` would NOT re-embed. Targets that ship the
# dashboard (all, redeploy) depend on this, not on `build`.
build-with-ui: web-build
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	@ln -sf $(BIN) $(BINARY)
	$(MAKE) plugins

all: build-with-ui

$(BIN): $(GO_SRC) go.mod go.sum
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	@# Self-healing symlink at the project root so `./talon` always
	@# points at the freshly built binary. Avoids the trap where a
	@# bare `go build ./cmd/talon` (or some other ancient build) left
	@# a stale ./talon next to ./bin/talon and the user runs the
	@# wrong one.
	@ln -sf $(BIN) $(BINARY)

# plugins builds the standalone helper CLIs (op, keychain) into bin/.
# First-party Go plugins are in the talon binary; no per-plugin build
# step is needed for them.
plugins: $(PLUGIN_BINS)

bin/talon-%-plugin: $(GO_SRC) go.mod go.sum
	$(GO) build -ldflags '$(LDFLAGS)' -o $@ ./apps/talon-$*-plugin

# install puts talon AND its sidecar plugins (talon-op-plugin) into GOBIN, so
# the installed `talon` resolves op:// secret refs — pluginSearchPaths looks
# next to the running binary, and `go install $(PKG)` alone would leave the op
# plugin behind in ./bin, breaking auth-token resolution for the PATH `talon`.
install:
	$(GO) install -ldflags '$(LDFLAGS)' $(PKG)
	$(GO) install -ldflags '$(LDFLAGS)' $(addprefix ./apps/talon-,$(addsuffix -plugin,$(PLUGINS)))

run: build
	$(BIN) $(ARGS)

gateway-run: build
	$(BIN) gateway run $(ARGS)

# dev-backend watches Go source (cmd/, internal/, go.mod) and rebuilds +
# bounces the gateway on every change. UI is left to Vite (make web-dev), so
# the gateway runs WITHOUT --web. Run this in its own terminal to iterate on
# the backend, or use `make dev` to launch it alongside Vite. Pass extra
# `gateway run` flags after `--`: make dev-backend ARGS="-- --verbose".
dev-backend:
	@scripts/dev-watch.sh $(ARGS)

# dev runs the backend watcher and the Vite dev server side-by-side, so BOTH
# halves hot-reload: Go changes rebuild+bounce the gateway (dev-watch.sh),
# frontend changes hot-reload via Vite. The Vite server proxies /ws, /healthz,
# and /talon.v1.* to the gateway (vite.config.ts), so visit
# http://127.0.0.1:5173 — not the gateway port — while iterating. Ctrl-C kills
# both via the EXIT trap.
dev:
	@echo "talon dev loop (both halves hot-reload):"
	@echo "  gateway: http://127.0.0.1:18789 (WS at /ws) — rebuilds on Go changes"
	@echo "  ui:      http://127.0.0.1:5173             — vite HMR"
	@trap 'kill 0' EXIT INT TERM; \
	  scripts/dev-watch.sh & \
	  $(MAKE) web-dev & \
	  wait

# dev-open opens the dashboard pointed at the Vite dev server (not the gateway
# port), with the gateway token baked into the URL fragment so auth works
# without --auth none. The gateway stays token-authed — important because Vite
# binds 0.0.0.0 for phone/LAN testing. From a phone, override the host with the
# Mac's LAN IP and skip the (local) browser launch, then open the printed URL:
#   make dev-open UI_HOST=http://10.0.0.228:5173 OPEN=--no-open
UI_HOST ?= http://localhost:5173
OPEN    ?=
dev-open: build
	@$(BIN) dashboard --ui-host $(UI_HOST) $(OPEN)

gateway-run-with-ui: build web-build
	$(BIN) gateway run --web $(WEB_DIST) $(ARGS)

# redeploy updates ALL talon binaries to the current source and bounces the
# running host gateway so the new build goes live in one step — the talon analog
# of `make redeploy` in mlx-stack. It updates both copies so they never drift:
#   - the local bin/ build (gateway binary with the embedded SvelteKit UI, via
#     build-with-ui), and
#   - the installed GOBIN copies (the PATH `talon` CLI + every plugin in
#     PLUGINS, e.g. talon-op-plugin, via `install`) — so the CLI that resolves
#     op:// auth tokens matches the gateway.
# talon has no launchd plist to bootout/bootstrap (the `talon gateway
# install/restart` service manager is still stubbed, talon-4an), so this manages
# the plain `talon gateway run` process directly: SIGTERM the old one (which
# reaps its plugin children on shutdown), wait for it to exit, then relaunch
# detached with output appended to a log file so it survives the terminal
# closing. Override the port/flags with GATEWAY_PORT / ARGS and the log path with
# GATEWAY_LOG. The dashboard (http://localhost:$(GATEWAY_PORT)/) is served from
# the embedded UI, so a plain `make redeploy` is enough — no --web flag needed.
# For live UI iteration without a rebuild, run the gateway under `make dev` +
# `make web-dev` (Vite) instead.
GATEWAY_PORT ?= 18789
GATEWAY_LOG  ?= $(HOME)/.talon/logs/gateway.log
redeploy: build-with-ui install
	@echo "==> stopping running gateway"
	@pkill -f '$(BINARY) gateway run' 2>/dev/null || true
	@i=0; while pgrep -f '$(BINARY) gateway run' >/dev/null 2>&1; do \
		i=$$((i+1)); \
		if [ $$i -ge 50 ]; then echo "timed out waiting for gateway to stop"; exit 1; fi; \
		sleep 0.1; \
	done
	@mkdir -p $(dir $(GATEWAY_LOG))
	@echo "==> starting gateway (port $(GATEWAY_PORT), log $(GATEWAY_LOG))"
	@nohup $(BIN) gateway run --port $(GATEWAY_PORT) $(ARGS) >> $(GATEWAY_LOG) 2>&1 & \
		echo "Redeployed $(BINARY) — new binary live (PID $$!, port $(GATEWAY_PORT))"

test:
	@TALON_BENCH=1 $(GO) test -p=1 ./...

# test-fast skips the benchmark regression gate. The gate (3% average
# threshold) requires serialized package execution to be precise;
# plain `go test ./...` skips it automatically because parallel-bench
# contention spikes timings 50%+. Use test-fast for tight iteration
# loops where you don't need the gate.
test-fast:
	$(GO) test -short ./...

# smoke is the fastest verification path: vets everything (catches
# build breaks across the tree) and runs a small set of pure-Go
# dispatch / naming unit tests that don't touch the filesystem,
# network, or sub-processes. Suitable after a single handler edit
# when you want a "did I break wiring?" check without paying the
# full server-package compile + 189-test runtime.
smoke:
	$(GO) vet ./...
	$(GO) test -count=1 -timeout=15s \
	    -run '^Test(NodeList|CommandsList|ModelsAuthStatus|Health|KeychainServiceForPath|MigratePlan|WriteFileRef|ParseFileRef|PluginConstructors)' \
	    ./internal/server ./cmd/talon

# End-to-end tests boot talon-gateway in a Docker container via
# testcontainers-go and exercise the full plugin lifecycle. Requires
# Docker; takes ~20-30s per run cold (image build) or ~5s warm.
# Build-tagged so `make test` stays fast.
test-e2e:
	$(GO) test -tags=e2e -count=1 -timeout=10m ./internal/e2e/...

# Microbenchmarks for the chat hot path, per-turn filesystem I/O,
# and config merge. Provider-side latency is excluded — every bench
# uses an in-process stub so we measure ONLY talon's own overhead.
# Run before/after suspect changes; diff with `benchstat`.
#
# Quick sanity loop (< 1s): make bench BENCHTIME=200x
# Stable numbers   (~30s): make bench BENCHTIME=5s
BENCHTIME ?= 1s
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -benchtime=$(BENCHTIME) \
	    ./internal/server/... \
	    ./internal/agentcontext/... \
	    ./internal/config/...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin

web-install:
	cd $(WEB_DIR) && $(PNPM) install

web-dev:
	cd $(WEB_DIR) && $(PNPM) run dev

web-build:
	cd $(WEB_DIR) && $(PNPM) run build

# Type-check the frontend (svelte-check). Mirrors `pnpm check`.
web-check:
	cd $(WEB_DIR) && $(PNPM) run check

# Frontend unit/integration tests (vitest, headless Chromium via Playwright).
# CI runs `pnpm exec playwright install --with-deps chromium` first; locally
# run `make web-test-install` once to fetch the browser.
web-test:
	cd $(WEB_DIR) && $(PNPM) run test

web-test-install:
	cd $(WEB_DIR) && $(PNPM) exec playwright install chromium

web: web-build

cross:
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-amd64       $(PKG)
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-arm64       $(PKG)
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-darwin-amd64      $(PKG)
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-darwin-arm64      $(PKG)
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-windows-amd64.exe $(PKG)

# ---- docker -----------------------------------------------------------
# Run talon-gateway inside a container so the bash/edit/write tools execute
# in an isolated filesystem instead of touching the host. Host port maps to
# 18789 by default; override DOCKER_HOST_PORT when needed.
DOCKER_IMAGE     ?= talon-gateway:dev
DOCKER_HOST_PORT ?= 18789
DOCKER_NAME      ?= talon-gateway

docker-build:
	docker build -t $(DOCKER_IMAGE) .

# Bind-mount ~/.talon at the SAME absolute path as on the host so workspace
# strings resolve transparently inside the container. HOME is propagated for
# the same reason.
# `--restart=unless-stopped` keeps the gateway up across crashes
# and host reboots while still respecting `make docker-stop` (or
# explicit `docker stop`) — the right semantics for an unattended
# box. Drops `--rm` since `--rm` and `--restart` are mutually
# exclusive; cleanup happens via the `docker rm -f` line below on
# the next `docker-run` invocation.
docker-run: docker-build
	@-docker rm -f $(DOCKER_NAME) >/dev/null 2>&1
	docker run -i -d --restart=unless-stopped \
	    --name $(DOCKER_NAME) \
	    -p $(DOCKER_HOST_PORT):18789 \
	    --add-host=host.docker.internal:host-gateway \
	    -e HOME=$(HOME) \
	    -v $(HOME)/.talon:$(HOME)/.talon \
	    $(DOCKER_IMAGE) $(ARGS)

docker-bounce: docker-stop docker-run

docker-stop:
	-docker stop $(DOCKER_NAME)

# Tail the gateway log stream. Docker's default json-file driver
# rotates at 10MB (configurable in daemon.json) and persists across
# container restarts thanks to --restart=unless-stopped, so this
# replaces the no-log-file gap for unattended ops.
docker-logs:
	docker logs $(DOCKER_NAME) --tail=200 -f

# ---- proto ------------------------------------------------------------
# Regenerates the gRPC plugin service stubs from the canonical .proto.
# Requires `protoc` (Homebrew: brew install protobuf) and the Go gen
# plugins (run `make proto-tools` to install them under $GOBIN).
PROTO_DIR := internal/plugin/proto
PROTO_OUT := internal/plugin/pb

proto:
	protoc --go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
	       --go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
	       -I$(PROTO_DIR) \
	       $(PROTO_DIR)/plugin.proto

# ---- connect api ------------------------------------------------------
# Regenerates the talon.v1.* Connect stubs from proto/talon/v1/*.proto.
# Distinct from `proto` above: that one builds the gRPC plugin protocol
# shipped to plugins; this one builds the gateway's outward-facing
# Connect API consumed by web/ and any future Go SDK (talon-y6v).
# Requires the `buf` CLI (brew install bufbuild/buf/buf) and the
# protoc-gen-connect-go plugin (run `make connect-tools`).
connect:
	buf generate

connect-tools:
	$(GO) install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

proto-tools:
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
