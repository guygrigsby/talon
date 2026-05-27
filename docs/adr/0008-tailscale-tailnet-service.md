# 0008 Tailscale tailnet bind via embedded tsnet node + VIPService

Status: Accepted

Date: 2026-05-26

## Context

talon already has a thin Tailscale path: `talon tailscale` and the
`--tailscale=serve|funnel` gateway flag shell out to `tailscale serve --bg
--https=443` (`cmd/talon/tailscale.go`). That path assumes the host already
runs an authenticated `tailscaled` and that the user has driven `tailscale up`
themselves. It exposes the gateway at the *machine's* own name
(`<machine>.<tailnet>.ts.net`), tied to whichever host happens to run it.

Two gaps:

1. **Setup is not self-contained.** A fresh box needs Tailscale installed,
   logged in, and serving before talon is reachable over the tailnet. There is
   no guided flow.
2. **`bind=tailnet` is declared but dead.** The config schema enumerates
   `bind: loopback|lan|tailnet|auto|custom`, but the gateway falls back to
   loopback with a warning for `tailnet` (`cmd/talon/gateway.go`), and
   `gateway.tailscale.mode` is read into config but never dispatched
   (`internal/talonconfig/native.go`).

We want users to stand talon up on a tailnet easily, with a **stable URL that
is independent of the host machine** — the address and port a client dials.
Tailscale's primitives for this are a **device/machine** (registered via a
tagged auth key) advertising a **Service** (VIPService: a named, virtual-IP
endpoint with its own MagicDNS name). Tailscale Services went GA in 2025 with
**native tsnet support**: a Go program can register as its own machine and
advertise a Service in-process via `Server.ListenService`, with no system
`tailscaled`, CLI, or config files.

## Decision

Add a first-party Tailscale integration that makes talon its own tailnet node
and advertises a Tailscale Service, plus a `talon configure tailscale` wizard
that provisions it via the Tailscale API.

### Not a gRPC plugin

The tsnet node *is* the gateway's network listener, so it must run in the
gateway process. It cannot be a `pb.PluginServer` subprocess like telegram or
brave (those offer tools/channels; they never provide the HTTP listener the
gateway serves on). The integration is therefore three in-tree pieces, not one
plugin:

- **`internal/tailscale`** — Tailscale API client. Mints OAuth access tokens
  from a stored OAuth client, creates the VIPService, mints a tagged auth key,
  reads the tailnet DNS name. Used at provision time.
- **`internal/tailnet`** — runtime. Brings up the tsnet node (state under
  `~/.talon/tailscale/`), calls `Server.ListenService("svc:talon",
  tsnet.ServiceModeHTTP{HTTPS: true, Port: <port>})`, and hands the resulting
  listener to the existing gateway mux. This is what finally wires
  `bind=tailnet`.
- **`cmd/talon/configure_tailscale.go`** — the wizard, registered in the
  existing `configureWizard` registry.

The word "plugin" in the original request is honored as a *capability*, not a
gRPC subprocess. A thin runtime status/ops surface may later be exposed as a
tool, but it is not required for this decision.

### Machine + Service + URL

- **Machine** = the embedded tsnet node, registered with a **tagged** auth key
  (`tag:talon`). Tagging is required for a node to advertise a Service.
- **Service** = a VIPService (`svc:talon`) created via the Tailscale API. The
  service must exist *before* a node can advertise it — tsnet advertises an
  existing service, it does not create one. Creating it is the API call that
  fulfills "create a service."
- **URL** = `listener.FQDN` from `ListenService`, i.e.
  `https://talon.<tailnet>.ts.net`, plus the service-defined port. This is the
  stable address+port, independent of the host.

### Credentials

The wizard collects a Tailscale **OAuth client** (id + secret), the
recommended automation credential. Stored through the backend-aware secret
path (keychain:// or op:// ref per ADR 0006 — never plaintext). Required
scopes (`auth_keys`, plus the services/devices scope) are confirmed during the
implementation spike against the live API and surfaced in the wizard. From the
OAuth client talon mints short-lived access tokens to create the service and
the tagged auth key.

### Provisioning flow (`talon configure tailscale`)

1. Collect OAuth client id+secret → store as a ref.
2. Mint a tagged auth key (`tag:talon`) via the API.
3. Create the VIPService (`svc:talon`) with the chosen port via the API.
4. **ACL grant**: advertising a Service requires a policy grant for the tag.
   talon does **not** silently edit the user's policy file. It prints the exact
   grant snippet and offers to apply it via the policy API only on explicit
   confirmation. Default is print-and-paste.
5. Write config (OAuth ref, tailnet name, `svc:talon`, port, derived FQDN), set
   `gateway.bind = "tailnet"`, and print the resulting URL.

### Runtime flow (`bind=tailnet`)

On gateway start with `bind=tailnet`, `internal/tailnet` brings up the tsnet
node, advertises the service, and serves the existing gateway HTTP mux (WS +
Connect RPC + embedded UI) on the service listener. `ln.FQDN` is logged as the
tailnet URL. Per the reload policy (talon-5zx), `gateway.bind` is a
restart-class path.

### Auth over the tailnet

talon's own token auth stays **on by default** as defense-in-depth, even though
Tailscale authenticates at the network layer. This keeps the
`talon dashboard` token model unchanged. Mapping Tailscale identity via tsnet
`WhoIs` into `auth=trusted-proxy` is a documented follow-up, not part of this
decision.

### Scope (v1)

Single service (`svc:talon`), single node, macOS + Linux. Token auth retained.
Manual-confirm ACL grant. No Funnel, no multi-backend service load-balancing,
no `trusted-proxy` identity mapping. The existing `tailscale serve` CLI-wrapper
path (`cmd/talon/tailscale.go`) is left untouched as the "use my own
tailscaled" escape hatch.

## Consequences

- **New heavyweight dependency**: `tailscale.com` (tsnet). BSD-3-Clause, so
  license-clean under the no-GPL/AGPL rule, but it is a large module that grows
  build size and the dependency surface. Accepted as the cost of a
  self-contained node with no system `tailscaled`.
- `bind=tailnet` becomes live; the loopback-fallback warning is removed for
  that mode.
- Provisioning is ordered before the first tailnet boot: the VIPService must
  exist before the node can advertise it.
- The wizard creates objects in the user's tailnet (a service, a tagged auth
  key) and may, with consent, edit the policy file. This is new outward-facing
  behavior gated behind explicit confirmation.
- Tailscale Services and the tsnet Service API are recent (2025 GA); exact
  OAuth scope names and the policy-API grant shape are validated by a spike at
  the start of implementation rather than assumed here.
- The existing CLI-wrapper path and the new tsnet path coexist; they are
  selected by different config (`gateway.tailscale.mode` serve/funnel vs
  `gateway.bind=tailnet`). A future ADR may unify or deprecate the wrapper.
