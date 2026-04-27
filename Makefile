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

.PHONY: build all install run gateway-run gateway-run-with-ui test vet fmt tidy clean cross web web-install web-dev web-build

build: $(BIN)

all: build web-build

$(BIN): $(GO_SRC) go.mod go.sum
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

install:
	$(GO) install -ldflags '$(LDFLAGS)' $(PKG)

run: build
	$(BIN) $(ARGS)

gateway-run: build
	$(BIN) gateway run $(ARGS)

gateway-run-with-ui: build web-build
	$(BIN) gateway run --web $(WEB_DIST) $(ARGS)

test:
	$(GO) test ./...

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
