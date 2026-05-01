# syntax=docker/dockerfile:1

# ---- builder ----------------------------------------------------------
# Pinned-minor Go matches go.mod's directive (go 1.26.x). Alpine variant
# keeps the build stage small and shares libc with the runtime stage.
FROM golang:1.26-alpine AS builder
WORKDIR /src

# Layer mod download separately from sources so iterating on code doesn't
# re-download dependencies every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off for a fully-static binary — we don't use any cgo dependencies
# and a static build runs in any base image including distroless if we
# ever shrink further.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/talon \
    ./cmd/talon

# Sibling Go-plugin binaries shipped alongside the gateway. Each one
# implements a self-contained gRPC plugin (see internal/plugin) so
# users can enable them per-config without a separate install step.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/talon-deepseek-plugin \
    ./apps/talon-deepseek-plugin
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/talon-telegram-plugin \
    ./apps/talon-telegram-plugin

# ---- shim install (Node) -------------------------------------------------
# openclaw-plugin-host is the Node subprocess that loads vendored
# openclaw extensions and bridges them to talon's gRPC plugin protocol.
# Installed under a fixed path so plugins.bundled.shimCmd defaults work
# without per-host configuration.
FROM node:20-alpine AS shim-install
WORKDIR /shim
COPY apps/openclaw-plugin-host/package.json ./package.json
RUN npm install --omit=dev --no-audit --no-fund
COPY apps/openclaw-plugin-host/ ./

# ---- runtime ----------------------------------------------------------
# Alpine instead of distroless because (a) the bash tool execs /bin/sh -c
# and the model is likely to invoke common Unix utilities, and (b) the
# openclaw-plugin-host shim needs Node. coreutils + git + grep + bash
# cover the typical surface; nodejs runs the shim subprocesses.
FROM alpine:3.20

RUN apk add --no-cache \
    ca-certificates \
    coreutils \
    grep \
    git \
    findutils \
    bash \
    nodejs \
    npm

COPY --from=builder /out/talon /usr/local/bin/talon
COPY --from=builder /out/talon-deepseek-plugin /usr/local/bin/talon-deepseek-plugin
COPY --from=builder /out/talon-telegram-plugin /usr/local/bin/talon-telegram-plugin
COPY --from=shim-install /shim /opt/openclaw-plugin-host
# Stable wrapper so plugins.bundled.shimCmd defaults can be a single
# string ("openclaw-plugin-host") that resolves via PATH.
RUN ln -s /opt/openclaw-plugin-host/bin/openclaw-plugin-host.mjs /usr/local/bin/openclaw-plugin-host

# Bundled openclaw extensions (vendored from openclaw@<sha>; see
# extensions/UPSTREAM.md). Users enable per-extension via
# plugins.entries.<name>.bundled = "<dir-name>" — the gateway expands
# that to a shim spawn against /opt/extensions/<dir-name>.
COPY extensions /opt/extensions

EXPOSE 18789

# Bind to all interfaces so Docker's port mapping can reach the listener;
# the host-side firewall remains the boundary.
ENTRYPOINT ["talon", "gateway", "run", "--bind=lan", "--port=18789"]
