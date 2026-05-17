BINARY := talon
PKG    := ./cmd/talon
BIN    := bin/$(BINARY)

LDFLAGS := -s -w

GO  ?= go
NPM ?= npm

# External openclaw control-ui (sibling repo). Override if your layout differs.
WEB_DIR  ?= ../openclaw/ui
WEB_DIST ?= ../openclaw/dist/control-ui

GO_SRC := $(shell find cmd internal -name '*.go' 2>/dev/null)

# First-party Go plugins (deepseek, telegram, brave, whisper,
# bluebubbles, mac-notify) ship inside the talon binary and run via
# `talon plugin run <name>` — no per-plugin binary to build. PLUGINS
# only lists the standalone helper CLIs (op for 1Password, keychain
# for macOS Keychain) that exist as independent processes.
PLUGINS := op keychain
PLUGIN_BINS := $(addprefix bin/talon-,$(addsuffix -plugin,$(PLUGINS)))

.PHONY: build all install run gateway-run gateway-run-with-ui plugins test test-e2e bench vet fmt tidy clean cross web web-install web-dev web-build docker-build docker-run docker-stop docker-bounce docker-logs proto proto-tools

build: $(BIN) plugins

all: build web-build

$(BIN): $(GO_SRC) go.mod go.sum
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

# plugins builds the standalone helper CLIs (op, keychain) into bin/.
# First-party Go plugins are in the talon binary; no per-plugin build
# step is needed for them.
plugins: $(PLUGIN_BINS)

bin/talon-%-plugin: $(GO_SRC) go.mod go.sum
	$(GO) build -ldflags '$(LDFLAGS)' -o $@ ./apps/talon-$*-plugin

install:
	$(GO) install -ldflags '$(LDFLAGS)' $(PKG)

run: build
	$(BIN) $(ARGS)

gateway-run: build
	$(BIN) gateway run $(ARGS)

gateway-run-with-ui: build web-build
	$(BIN) gateway run --web $(WEB_DIST) $(ARGS)

test:
	@TALON_BENCH=1 $(GO) test -p=1 ./...

# test-fast skips the benchmark regression gate. The gate (3% average
# threshold) requires serialized package execution to be precise;
# plain `go test ./...` skips it automatically because parallel-bench
# contention spikes timings 50%+. Use test-fast for tight iteration
# loops where you don't need the gate.
test-fast:
	$(GO) test -short ./...

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
	cd $(WEB_DIR) && $(NPM) install

web-dev:
	cd $(WEB_DIR) && $(NPM) run dev

web-build:
	cd $(WEB_DIR) && $(NPM) run build

web: web-build

cross:
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-amd64       $(PKG)
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-arm64       $(PKG)
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-darwin-amd64      $(PKG)
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-darwin-arm64      $(PKG)
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-windows-amd64.exe $(PKG)

# ---- docker -----------------------------------------------------------
# Run talon-gateway inside a container so the bash/edit/write tools execute
# in an isolated filesystem instead of touching the host. Host port maps
# to the original openclaw port (18789) — stop any running openclaw
# gateway first or override DOCKER_HOST_PORT.
DOCKER_IMAGE     ?= talon-gateway:dev
DOCKER_HOST_PORT ?= 18789
DOCKER_NAME      ?= talon-gateway

docker-build:
	docker build -t $(DOCKER_IMAGE) .

# Bind-mount ~/.openclaw and ~/.talon at the SAME absolute paths as on the
# host so agents.list[].workspace strings (which embed host paths like
# "$$HOME/.openclaw/workspace") resolve transparently inside the
# container. HOME is propagated for the same reason.
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
	    -v $(HOME)/.openclaw:$(HOME)/.openclaw \
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

proto-tools:
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
