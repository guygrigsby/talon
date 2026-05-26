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

# Secret resolver helper shipped alongside the gateway.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/talon-op-plugin \
    ./apps/talon-op-plugin

# ---- runtime ----------------------------------------------------------
# Alpine instead of distroless because (a) the bash tool execs /bin/sh -c
# and the model is likely to invoke common Unix utilities. coreutils + git +
# grep + bash cover the typical surface.
FROM alpine:3.20

RUN apk add --no-cache \
    ca-certificates \
    coreutils \
    grep \
    git \
    findutils \
    bash

COPY --from=builder /out/talon /usr/local/bin/talon
COPY --from=builder /out/talon-op-plugin /usr/local/bin/talon-op-plugin

EXPOSE 18789

# Bind to all interfaces so Docker's port mapping can reach the listener;
# the host-side firewall remains the boundary.
ENTRYPOINT ["talon", "gateway", "run", "--bind=lan", "--port=18789"]
