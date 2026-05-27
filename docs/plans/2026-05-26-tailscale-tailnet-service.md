# Tailscale tailnet bind: tsnet node + VIPService + wizard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Status: Ready to implement (handoff)
Date: 2026-05-26
Tracks: beads `talon-9ob` • ADR `docs/adr/0008-tailscale-tailnet-service.md`

**Goal:** Let a user run `talon configure tailscale` to provision a Tailscale VIPService and stand the gateway up on the tailnet at a stable `https://talon.<tailnet>.ts.net` URL, served by an embedded tsnet node wired to `gateway.bind=tailnet`.

**Architecture:** Three in-tree pieces (per ADR 0008): `internal/tailscale` (Tailscale API client, provision-time), `internal/tailnet` (tsnet runtime that owns the gateway's listener), and `cmd/talon/configure_tailscale.go` (wizard). Not a gRPC plugin — the tsnet node *is* the gateway listener, so it runs in-process. Token auth stays on; ACL grant is print-and-confirm, never silent.

**Tech Stack:** Go, `tailscale.com/tsnet` (NEW dep, BSD-3), Tailscale REST API v2 (OAuth client-credentials), cobra wizard, `internal/secrets` keychain refs, `internal/config` dotted-path writes.

---

## Context

Current state (verified):

- `cmd/talon/tailscale.go` is a CLI wrapper around `tailscale serve` (off/serve/funnel). Left untouched by this plan.
- `gateway.bind=tailnet` is in the schema enum (`internal/config/schema_test.go:25`) but `cmd/talon/gateway.go` falls back to loopback with a warning for it. `gateway.tailscale.mode` is read in `internal/talonconfig/native.go` but never dispatched.
- First-party plugins register in `cmd/talon/plugin_run.go` + `internal/server/plugin_deps.go`. **This feature is NOT one of those** — no entry there.
- Wizards register in `configureWizardsForTest` (`cmd/talon/configure.go:101`); each is `func(in io.Reader, out io.Writer) error`. Secret pattern: `secrets.StoreKeychainSecret(ctx, target, secret) (ref, err)` → `config.Set(paths, path, ref, SetReplaceSafe)`. The backend-aware `acquireSecretRef` helper from `docs/plans/2026-05-26-cli-secret-config-commands.md` is **not yet landed**, so this wizard uses `StoreKeychainSecret` directly (Follow-ups notes the migration).
- `tailscale.com` is not yet a dependency.

Tailscale facts the design rests on (confirm exact signatures in Task 0):

- tsnet: `s := &tsnet.Server{Hostname, Dir, AuthKey}`; `ln, err := s.ListenService("svc:talon", tsnet.ServiceModeHTTP{HTTPS: true, Port: 443})`; `ln.FQDN` → the URL host.
- A VIPService must be **created first** via the API/admin; tsnet only advertises an existing service.
- The node's auth key must be **tagged** (`tag:talon`) for the node to advertise a service, and an ACL grant is required.

## File structure

| File | New/Mod | Responsibility |
|---|---|---|
| `go.mod` / `go.sum` | mod | add `tailscale.com` |
| `internal/talonconfig/native.go` | mod | add tailnet/service config fields to `GatewayConfig` |
| `internal/config/reload.go` | mod | classify `gateway.bind`, `gateway.tailscale.*` as restart |
| `internal/tailscale/client.go` | new | API client: OAuth token, create service, mint tagged key, tailnet name |
| `internal/tailscale/client_test.go` | new | httptest-backed unit tests |
| `internal/tailnet/serve.go` | new | tsnet node bring-up + `ListenService`, returns `net.Listener` + FQDN |
| `internal/tailnet/serve_test.go` | new | option validation; env-gated integration test |
| `cmd/talon/gateway.go` | mod | wire `bind=tailnet` to `internal/tailnet`; drop the loopback fallback for it |
| `cmd/talon/gateway_test.go` | mod | bind=tailnet selects the tailnet listener path (injected factory) |
| `cmd/talon/configure_tailscale.go` | new | the wizard + `configureTailscaleCmd` |
| `cmd/talon/configure_tailscale_test.go` | new | scripted-stdin wizard tests with fakes |
| `cmd/talon/configure.go` | mod | register the wizard in `configureWizardsForTest` |
| `docs/dependencies.md` | mod | document the tsnet dependency + scope boundary |

---

## Task 0: Spike — pin tsnet + Tailscale API contracts

**Goal:** Replace doc-derived assumptions with verified signatures and scope names before writing real code. Timebox ~2h. Output: a "Findings" subsection appended to this plan and any correction to ADR 0008.

**Files:**
- Create (throwaway, deleted at end): `cmd/tsnet-spike/main.go`

- [ ] **Step 1: Add the dependency**

```bash
cd ~/projects/talon
go get tailscale.com@latest
go mod tidy
```
Expected: `tailscale.com` appears in `go.mod`.

- [ ] **Step 2: Confirm tsnet Service API signature**

Write `cmd/tsnet-spike/main.go` that reads `TS_AUTHKEY` (a tagged key you mint by hand in the admin console for `tag:talon`) and a pre-created `svc:talon` service, then:

```go
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"tailscale.com/tsnet"
)

func main() {
	s := &tsnet.Server{Hostname: "talon-spike", Dir: "/tmp/talon-spike-state", AuthKey: os.Getenv("TS_AUTHKEY")}
	defer s.Close()
	ln, err := s.ListenService("svc:talon", tsnet.ServiceModeHTTP{HTTPS: true, Port: 443})
	if err != nil {
		log.Fatalf("ListenService: %v", err)
	}
	fmt.Println("FQDN:", ln.FQDN())
	log.Fatal(http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})))
}
```
Run: `TS_AUTHKEY=tskey-auth-... go run ./cmd/tsnet-spike`
Expected: prints an FQDN like `talon.<tailnet>.ts.net`; `curl https://talon.<tailnet>.ts.net` from another tailnet device returns `ok`.

**Record in Findings:** exact `ListenService` signature, exact `ServiceModeHTTP` field names, whether `FQDN` is a field or method, and the `tsnet.Server` field used for the state dir (`Dir`).

- [ ] **Step 3: Confirm the API contracts via curl**

Using a Tailscale OAuth client (id+secret) you create in the admin console with scopes `auth_keys` (write) and the services scope:

```bash
# OAuth client-credentials token
TOKEN=$(curl -s -u "$OAUTH_ID:$OAUTH_SECRET" \
  -d 'grant_type=client_credentials' \
  https://api.tailscale.com/api/v2/oauth/token | jq -r .access_token)

# mint a tagged auth key
curl -s -H "Authorization: Bearer $TOKEN" \
  https://api.tailscale.com/api/v2/tailnet/-/keys \
  -d '{"capabilities":{"devices":{"create":{"reusable":true,"ephemeral":false,"tags":["tag:talon"]}}}}'

# create the VIPService (CONFIRM exact path/verb/body — this is the least-documented call)
# try: PUT /api/v2/tailnet/-/services/svc:talon  with a ports body
```
**Record in Findings:** the exact endpoint, verb, and JSON body that creates a VIPService; the exact scope names that worked; the tailnet-name endpoint (`GET /api/v2/tailnet/-` or similar); and the policy-API grant snippet shape needed for the tag to advertise the service.

- [ ] **Step 4: Write the Findings subsection** at the bottom of this file (heading `## Findings (Task 0)`), then delete the spike: `rm -rf cmd/tsnet-spike /tmp/talon-spike-state`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum docs/plans/2026-05-26-tailscale-tailnet-service.md docs/adr/0008-tailscale-tailnet-service.md
git commit -m "talon-9ob: add tailscale.com dep + pin tsnet/API contracts (spike)"
```

> **All later tasks reference the Findings names.** Where a signature below differs from Findings, Findings wins.

---

## Task 1: Config fields + reload classification

**Files:**
- Modify: `internal/talonconfig/native.go` (`GatewayConfig`, around lines 26-36 and the populate around line 181)
- Modify: `internal/config/reload.go`
- Test: `internal/config/reload_test.go`

- [ ] **Step 1: Write failing reload test**

In `internal/config/reload_test.go`, add cases to the existing table:
```go
{"gateway.bind", ReloadRestart},
{"gateway.tailscale.service", ReloadRestart},
{"gateway.tailscale.oauth_client_ref", ReloadRestart},
```

- [ ] **Step 2: Run it, verify failure**

Run: `go test ./internal/config/ -run TestClassifyReload -v`
Expected: FAIL (paths classify as `ReloadNextRPC`, not `ReloadRestart`).

- [ ] **Step 3: Add the paths to the ReloadRestart list** in `internal/config/reload.go` (append `gateway.bind`, `gateway.tailscale.service`, `gateway.tailscale.oauth_client_ref` to the restart-class set).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/config/ -run TestClassifyReload -v`
Expected: PASS.

- [ ] **Step 5: Extend `GatewayConfig`** in `internal/talonconfig/native.go`:
```go
// Tailnet service bind (ADR 0008). Populated from gateway.tailscale.*.
TailnetService      string `mapstructure:"-"` // e.g. "svc:talon"
TailnetOAuthClientID string `mapstructure:"-"` // non-secret OAuth client id (plaintext)
TailnetOAuthRef     string `mapstructure:"-"` // keychain://… or op://… ref to the OAuth secret
TailnetName         string `mapstructure:"-"` // <tailnet>.ts.net, cached at provision
```
And populate them next to the existing `TailscaleMode` line (~181):
```go
TailnetService:       gjson.GetBytes(raw, "gateway.tailscale.service").Str,
TailnetOAuthClientID: gjson.GetBytes(raw, "gateway.tailscale.oauth_client_id").Str,
TailnetOAuthRef:      gjson.GetBytes(raw, "gateway.tailscale.oauth_client_ref").Str,
TailnetName:          gjson.GetBytes(raw, "gateway.tailscale.tailnet").Str,
```

- [ ] **Step 6: Build + commit**

Run: `make build && go test ./internal/config/ ./internal/talonconfig/`
```bash
git add internal/config/reload.go internal/config/reload_test.go internal/talonconfig/native.go
git commit -m "talon-9ob: config fields + restart classification for tailnet bind"
```

---

## Task 2: `internal/tailscale` API client

**Files:**
- Create: `internal/tailscale/client.go`
- Test: `internal/tailscale/client_test.go`

Interface (adjust endpoints/bodies to Task 0 Findings):
```go
package tailscale

type Client struct {
	httpc   *http.Client
	apiBase string // default https://api.tailscale.com
	token   string // bearer access token
}

// NewFromOAuth exchanges an OAuth client id+secret for an access token.
func NewFromOAuth(ctx context.Context, id, secret string) (*Client, error)

// TailnetName returns the tailnet's MagicDNS base, e.g. "example.ts.net".
func (c *Client) TailnetName(ctx context.Context) (string, error)

// MintAuthKey creates a reusable, tagged auth key for node registration.
func (c *Client) MintAuthKey(ctx context.Context, tag string) (string, error)

// CreateService creates a VIPService (idempotent: returns nil if it exists).
func (c *Client) CreateService(ctx context.Context, name string, ports []int) error
```

- [ ] **Step 1: Write failing tests** in `internal/tailscale/client_test.go` using `httptest.NewServer`. One test per method. Example for the token exchange:
```go
func TestNewFromOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oauth/token" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
			t.Fatalf("grant_type missing: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok-123","token_type":"Bearer"}`)
	}))
	defer srv.Close()
	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "tok-123" {
		t.Fatalf("token = %q", c.token)
	}
}
```
(Use an unexported `newFromOAuthAt(ctx, base, id, secret)` so tests can point at the httptest URL; `NewFromOAuth` calls it with the real base.) Mirror for `MintAuthKey` (assert tagged-key request body + parses `{"key":"tskey-auth-..."}`), `CreateService` (assert verb/path/body from Findings; treat 409/already-exists as success), and `TailnetName`.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/tailscale/ -v`
Expected: FAIL (package/functions undefined).

- [ ] **Step 3: Implement `client.go`** with `newFromOAuthAt` doing the form-POST token exchange, the bearer-authed helpers, and a small `do(ctx, method, path, body, out)` JSON helper. Use endpoints/bodies from Findings. `CreateService` swallows the already-exists status.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/tailscale/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tailscale/
git commit -m "talon-9ob: Tailscale API client (oauth, auth key, service, tailnet)"
```

---

## Task 3: `internal/tailnet` runtime (tsnet node + ListenService)

**Files:**
- Create: `internal/tailnet/serve.go`
- Test: `internal/tailnet/serve_test.go`

Interface:
```go
package tailnet

type Options struct {
	Hostname string // tsnet node name, e.g. "talon"
	StateDir string // ~/.talon/tailscale
	AuthKey  string // tagged auth key (only needed on first registration)
	Service  string // "svc:talon"
	Port     int    // service port
}

type Listener struct {
	net.Listener
	FQDN string // e.g. talon.example.ts.net
	close func() error
}

func (l *Listener) Close() error { return l.close() }

// Serve brings up a tsnet node and returns a listener advertising the service.
func Serve(ctx context.Context, o Options) (*Listener, error)
```

- [ ] **Step 1: Write failing option-validation tests** in `serve_test.go` (these run without a tailnet):
```go
func TestServeRejectsEmptyService(t *testing.T) {
	_, err := Serve(context.Background(), Options{Hostname: "talon", StateDir: t.TempDir(), Port: 443})
	if err == nil {
		t.Fatal("want error for empty service")
	}
}
func TestServeRejectsBadServicePrefix(t *testing.T) {
	_, err := Serve(context.Background(), Options{Service: "talon", Hostname: "talon", StateDir: t.TempDir(), Port: 443})
	if err == nil || !strings.Contains(err.Error(), "svc:") {
		t.Fatalf("want svc: prefix error, got %v", err)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/tailnet/ -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `serve.go`.** Validate `Service` is non-empty and `strings.HasPrefix(o.Service, "svc:")` and `o.Port > 0` *before* touching tsnet (so the validation tests pass offline). Then construct `&tsnet.Server{Hostname, Dir: StateDir, AuthKey}`, call `ListenService(o.Service, tsnet.ServiceModeHTTP{HTTPS: true, Port: o.Port})` (exact per Findings), wrap the returned listener and its `FQDN`, and set `close` to close both the listener and `s.Close()`.

- [ ] **Step 4: Run, verify validation tests pass**

Run: `go test ./internal/tailnet/ -v`
Expected: PASS (the two offline tests).

- [ ] **Step 5: Add an env-gated integration test** that only runs with a real key:
```go
func TestServeIntegration(t *testing.T) {
	key := os.Getenv("TALON_TEST_TS_AUTHKEY")
	if key == "" {
		t.Skip("set TALON_TEST_TS_AUTHKEY (tagged) to run")
	}
	ln, err := Serve(context.Background(), Options{Hostname: "talon-test", StateDir: t.TempDir(), AuthKey: key, Service: "svc:talon-test", Port: 443})
	if err != nil { t.Fatal(err) }
	defer ln.Close()
	if !strings.HasSuffix(ln.FQDN, ".ts.net") { t.Fatalf("FQDN = %q", ln.FQDN) }
}
```

- [ ] **Step 6: Commit**
```bash
git add internal/tailnet/
git commit -m "talon-9ob: tsnet runtime — ListenService + FQDN listener"
```

---

## Task 4: Wire `bind=tailnet` in the gateway

**Files:**
- Modify: `cmd/talon/gateway.go` (the bind switch ~lines 113-122 and the listener setup)
- Test: `cmd/talon/gateway_test.go`

- [ ] **Step 1: Write failing test** asserting that `bind=tailnet` takes the tailnet listener path. Inject a package-level factory so the test avoids real tsnet:
```go
// in gateway.go: var tailnetServe = tailnet.Serve  (injectable)
func TestBindTailnetUsesTailnetServe(t *testing.T) {
	called := false
	old := tailnetServe
	tailnetServe = func(ctx context.Context, o tailnet.Options) (*tailnet.Listener, error) {
		called = true
		if o.Service != "svc:talon" { t.Fatalf("service = %q", o.Service) }
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		return &tailnet.Listener{Listener: l, FQDN: "talon.example.ts.net"}, nil
	}
	defer func() { tailnetServe = old }()
	// drive the listener-selection helper with bind=tailnet config; assert called.
}
```
(Refactor the bind→listener decision into a small testable helper, e.g. `gatewayListener(ctx, merged) (net.Listener, fqdn string, err error)`, if it isn't already isolated.)

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/talon/ -run TestBindTailnet -v`
Expected: FAIL.

- [ ] **Step 3: Implement.** In the bind switch, add a `case "tailnet":` that resolves `gateway.tailscale.oauth_client_ref` is not required at runtime (node persists via state dir) — read `gateway.tailscale.service` + `gateway.port`, resolve any auth key from state, call `tailnetServe(...)`, serve the existing mux on `ln`, log `ln.FQDN`. **Remove** `tailnet` from the loopback-fallback `default` warning branch.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./cmd/talon/ -run TestBindTailnet -v`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add cmd/talon/gateway.go cmd/talon/gateway_test.go
git commit -m "talon-9ob: wire gateway bind=tailnet to tsnet listener"
```

---

## Task 5: `talon configure tailscale` wizard

**Files:**
- Create: `cmd/talon/configure_tailscale.go`
- Test: `cmd/talon/configure_tailscale_test.go`
- Modify: `cmd/talon/configure.go:101` (registry)

Model on `configureTelegram` (`configure.go:280-402`). Injectable seams for tests:
```go
// configure_tailscale.go
var newTailscaleClient = tailscale.NewFromOAuth          // (ctx,id,secret)->*Client
var storeTailscaleSecret = secrets.StoreKeychainSecret   // (ctx,target,secret)->ref
```

- [ ] **Step 1: Write failing wizard test** in `configure_tailscale_test.go`. Use a fake client + fake storer, scripted stdin, temp `TALON_STATE_DIR`; assert config writes (ref not plaintext, `gateway.bind=tailnet`, `gateway.tailscale.service=svc:talon`) and that the printed output contains the FQDN and the ACL grant snippet. Pattern from `configure_telegram_test.go` / `patchWizards` (`configure_menu_test.go:22`).

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/talon/ -run TestConfigureTailscale -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `configureTailscale(in, out)`** with these steps:
  1. Prompt for OAuth client id, then secret (accept `TS_OAUTH_CLIENT_ID`/`TS_OAUTH_CLIENT_SECRET` env like Telegram accepts `TELEGRAM_BOT_TOKEN`).
  2. `c := newTailscaleClient(ctx, id, secret)`; `tailnet, _ := c.TailnetName(ctx)` — verifies creds; print `✓ Connected to tailnet <name>`.
  3. Prompt service name (default `talon`); normalize to `svc:talon`. Prompt port (default 443).
  4. `c.CreateService(ctx, "svc:talon", []int{port})`; print `✓ Service svc:talon ready`.
  5. `key := c.MintAuthKey(ctx, "tag:talon")`.
  6. **ACL grant:** print the exact grant snippet (from Findings) and prompt `Apply this grant to your tailnet policy now? [y/N]`. Only on `y`, call the policy-API apply (add `Client.EnsureGrant` in Task 2 if Findings supports it); otherwise instruct the user to paste it. Never auto-apply on default.
  7. Store the OAuth **secret** as a ref (the client id is not sensitive and is written plaintext): `ref := storeTailscaleSecret(ctx, "talon.gateway.tailscale.oauthSecret", secret)`. Then `config.Set` the writes:
```go
writes := []struct{ path []string; value any }{
	{[]string{"gateway", "tailscale", "oauth_client_id"}, id},   // plaintext (not a secret)
	{[]string{"gateway", "tailscale", "oauth_client_ref"}, ref}, // keychain:// ref to the secret
	{[]string{"gateway", "tailscale", "service"}, "svc:talon"},
	{[]string{"gateway", "tailscale", "tailnet"}, tailnet},
	{[]string{"gateway", "port"}, port},
	{[]string{"gateway", "bind"}, "tailnet"},
}
```
  8. Persist the minted auth key into the tsnet state dir path or write `gateway.tailscale.auth_key_ref` (store via keychain) so first boot can register. Print the final URL: `https://talon.<tailnet>.ts.net` and a restart hint (`emitReloadHint`-style; `gateway.bind` is restart-class).

- [ ] **Step 4: Register the wizard** in `configure.go:101`:
```go
{Kind: "tailscale", Name: "tailscale", Aliases: []string{"ts"}, Label: "Tailscale (tailnet service)", Run: configureTailscale},
```
Add `configureTailscaleCmd()` mirroring `configureChannelCmd()` so `talon configure tailscale` runs directly, and ensure it appears on the top-level `talon configure` menu.

- [ ] **Step 5: Run, verify pass**

Run: `go test ./cmd/talon/ -run TestConfigureTailscale -v`
Expected: PASS.

- [ ] **Step 6: Commit**
```bash
git add cmd/talon/configure_tailscale.go cmd/talon/configure_tailscale_test.go cmd/talon/configure.go
git commit -m "talon-9ob: talon configure tailscale wizard (provision service + bind)"
```

---

## Task 6: Docs + dependency note

**Files:**
- Modify: `docs/dependencies.md`

- [ ] **Step 1: Add a `tailscale.com (tsnet)` entry** to `docs/dependencies.md`: what it's for (embedded tailnet node + VIPService advertisement), scope boundary (runtime networking only, in `internal/tailnet`; provisioning in `internal/tailscale`), license (BSD-3), and that it's selected by `gateway.bind=tailnet` (distinct from the legacy `tailscale serve` wrapper).

- [ ] **Step 2: Commit**
```bash
git add docs/dependencies.md
git commit -m "talon-9ob: document tsnet dependency + scope"
```

---

## Verification

```bash
make build
go test ./internal/config/... ./internal/talonconfig/... ./internal/tailscale/... ./internal/tailnet/... ./cmd/talon/...
make vet
```
All green. Then the env-gated integration test with a real tagged key:
```bash
TALON_TEST_TS_AUTHKEY=tskey-auth-... go test ./internal/tailnet/ -run TestServeIntegration -v
```

Manual e2e (needs a real tailnet + OAuth client):
1. `talon configure tailscale` → enter OAuth id/secret → confirms tailnet, creates `svc:talon`, prints ACL grant (decline auto-apply, paste it in the admin console), writes config, prints `https://talon.<tailnet>.ts.net`.
2. Verify in the admin console: service `svc:talon` exists; a `tag:talon` device is registered.
3. `talon gateway run` (bind is now `tailnet`) boots, logs the FQDN.
4. From another tailnet device: `talon dashboard --ui-host https://talon.<tailnet>.ts.net` opens the UI; token auth still required (defense-in-depth confirmed).
5. Confirm no plaintext OAuth secret in `~/.talon/config.toml`; `gateway.tailscale.oauth_client_ref` is a `keychain://` ref.

## Follow-ups (file as beads issues during execution)

- Migrate the wizard's secret storage onto the backend-aware `acquireSecretRef`/`storeSecret` helper once `docs/plans/2026-05-26-cli-secret-config-commands.md` lands, so the OAuth client can live in 1Password too.
- `auth=trusted-proxy` via tsnet `WhoIs`: map Tailscale identity so the tailnet bind can drop the separate talon token (ADR 0008 names this a follow-up).
- `talon tailscale` (CLI-wrapper) vs `bind=tailnet` unification or deprecation — a future ADR.
- Windows support for the wizard's keychain step (keychain is macOS-only; needs the op backend or a Windows credential store).

---

## Findings (Task 0)

Pinned from official sources without a live token (the spike's code/curl steps were skipped — repo had other agents active). **Where these differ from Tasks 1-6 above, Findings wins.**

### tsnet runtime ([pkg.go.dev/tailscale.com/tsnet](https://pkg.go.dev/tailscale.com/tsnet))
```go
type Server struct {
	Dir, Hostname, AuthKey string
	Ephemeral              bool
	ClientID, ClientSecret string   // OAuth creds accepted directly
	AdvertiseTags          []string // e.g. ["tag:talon"]
}
func (s *Server) ListenService(name string, mode ServiceMode) (*ServiceListener, error)
type ServiceModeHTTP struct { Port uint16; HTTPS bool; AcceptAppCaps map[string][]string; PROXYProtocolVersion int }
type ServiceListener struct { net.Listener; FQDN string }
```
`ListenService` takes the **`svc:`-prefixed** name; `ln.FQDN` is a string field.

### VIPService API (Tailscale Go client, [PR #14539](https://github.com/tailscale/tailscale/pull/14539/files))
- `PUT|GET|DELETE /api/v2/tailnet/{tailnet}/vip-services/by-name/{name}` (name URL-escaped).
- Body: `VIPService{ Name string; Addrs []string; Comment string; Ports []string; Tags []string }`.
- API `Name` is a **bare label** (`talon`), NOT `svc:`-prefixed (opposite of tsnet).
- Upstream methods: `GetVIPServiceByName`, `CreateOrUpdateVIPServiceByName`, `DeleteVIPServiceByName`.

### OAuth ([oauth-clients](https://tailscale.com/kb/1215/oauth-clients))
- Token: `POST https://api.tailscale.com/api/v2/oauth/token`, `client_id`+`client_secret` (client_credentials).
- Scopes: `auth_keys` (requires tags), `devices:core`. **Residual gap:** the exact scope for the vip-services endpoints isn't documented; confirm with a live token (likely `devices:core` or a `services` scope). This is the only item still needing real credentials.

### ACL grants ([tailscale-services](https://tailscale.com/docs/features/tailscale-services)) — wizard prints these:
```jsonc
{ "src": ["autogroup:member"], "dst": ["svc:talon"], "ip": ["443"] }
"autoApprovers": { "services": { "svc:talon": ["tag:talon"] } }
```

### Corrections to the tasks above
1. **Drop the auth-key mint.** tsnet registers from `ClientID`/`ClientSecret` + `AdvertiseTags` directly, so `internal/tailnet` resolves the OAuth client at boot and passes it to tsnet — no separately-minted auth key. Remove `MintAuthKey` (Task 2) and the auth-key steps in Task 5. `internal/tailnet.Options` carries `ClientID, ClientSecret, AdvertiseTags` instead of `AuthKey`.
2. **Wrap the upstream client where practical.** Task 2 prefers `tailscale.com/client/tailscale` VIPService methods over hand-rolled HTTP (no-reimplementation rule). If that package can't be imported cleanly standalone, fall back to a minimal HTTP client — decided at implementation time.
3. **`Ports` is `[]string`** (e.g. `"443"`/`"tcp:443"`), not `[]int`. Fix `CreateService`.
4. **Name-form helper.** `svcName(bare) -> "svc:"+bare` for tsnet; `bareName(svc) -> strings.TrimPrefix(svc,"svc:")` for the API. Store the bare label in config and prefix for tsnet.

ADR 0008's "mint a tagged auth key" step is superseded by correction 1 (OAuth-direct node registration); the service-must-pre-exist ordering still holds.
