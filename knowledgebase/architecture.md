# MockNetPack Server — Architecture (L2)

> KB layer L2. Read [`overview.md`](overview.md) first. Next: [`code-map.md`](code-map.md).
> This is the most detailed KB of the three ends — the server is the product's core.

## High-level architecture

```
                       ┌─────────────────────  mockd binary  ─────────────────────┐
                       │                                                           │
  SDK (iOS) ──/api/v1──▶  ADMIN SERVER  :4290                                      │
  Web UI    ──/api/v1──▶  ├─ /api/v1 capture API   (custom handlers)               │
  Browser   ──/mocknetpack/▶ ├─ /api/v1/auth/*         (custom)                │
                       │  ├─ /api/v1/shares/*       (custom)                 │
                       │  ├─ /mocknetpack/ static   (custom web serving)           │
                       │  └─ upstream admin API     (mocks, workspaces, ...)       │
                       │                  │                                        │
                       │                  ▼                                        │
                       │  STORE LAYER: pkg/store + pkg/store/file (file-backed)    │
                       │    DeviceStore · CaptureSessionStore · MockRuleStore      │
                       │    UserStore · AuthSessionStore                           │
                       │    CaptureManager (runtime semantics + in-memory traffic) │
                       │                                                           │
                       │  ENGINE SERVER :4280  (upstream data plane: serves mocks, │
                       │    recording, MCP ...; not used by capture/mock execution)│
                       └───────────────────────────────────────────────────────────┘
```

## Package layering (custom vs upstream)

```
cmd/mockd ───────────────► pkg/cli (start.go wires custom options) ──► pkg/admin (API)
   │                                                                      │
   │                                    ┌─────────────────────────────────┤
   │                                    ▼                                 ▼
   │                          custom handlers          upstream handlers
   │                          (capture/auth/rules/shares) (mocks/engine/...)
   ▼
pkg/capture (models) ◄── pkg/store.CaptureManager ◄── pkg/store interfaces
        ▲                       │       ▲                       │
        │                       │       └────── pkg/store/file (persistence)
        │                       ▼
        └── pkg/account (User/AuthSession + PBKD) ── used by pkg/admin auth
```

| Layer | Files | Responsibility |
|---|---|---|
| Entry | `cmd/mockd/main.go` | `cli.Execute()` |
| CLI | `pkg/cli/start.go` | Cobra `start` command; binds MockNetPack flags; builds `NewAPI` with `WithCaptureConfig`, `WithWebDir`, `WithDataDir`, `WithAPIKey...`, `--no-auth`, `--create-admin` |
| Model | `pkg/capture/types.go` | `Device`, `CaptureSession`, `TrafficEntry`, `MockRule*`, `ServerConfig`, `ViewerLease` — pure data + derived helpers (`DeriveDeviceStatus`) |
| Domain | `pkg/store/*_registry.go` (`capture`, `device`, `session`, `traffic`, `rule`, `share`) | `CaptureManager`: all runtime semantics (heartbeat, session lifecycle, viewer leases, traffic, rules, conflicts, janitors, shares) |
| Store interfaces | `pkg/store/interfaces.go` | `DeviceStore`, `CaptureSessionStore`, `MockRuleStore`, `UserStore`, `AuthSessionStore`, … (persistence contract) |
| Persistence | `pkg/store/file/*.go` | File-backed implementations of the stores (devices/sessions/rules/accounts/etc.) |
| Account | `pkg/account/account.go` | `User`, `Role`, `AuthSession`, PBKD-SHA256 hashing, `NewToken` |
| HTTP (custom) | `pkg/admin/{capture_handlers,device_handlers,traffic_handlers,auth_handlers,auth_middleware,mock_rule_handlers,mocknetpack_web}.go` + `routes.go` + `api.go` | `/api/v1` handlers, auth gate, ownership filtering, static web serving |
| HTTP (upstream) | rest of `pkg/admin/*.go` | mockd native admin API (mocks, workspaces, engines, proxy, recording, chaos, …) |
| Contract | `openapi/mocknetpack.yaml` | `/api/v1` schemas + paths (v0.8.0: adds `/pairing-tokens`, register `pairingToken`/`deviceName`, `403 pairing_token_invalid`) |

## Domain flows

### 1. Device registration & heartbeat

```
Web manual registration (only creation path)
  POST /api/v1/devices  {app, did, name}            [requireAuth]
    → allowedApps check (catalog) → owner = current user
    → CaptureManager.CreateManualDevice (no upsert; (App,did) conflict ⇒ 409 device_taken)

SDK register (metadata refresh only; pairingToken enables auto-create)
  POST /api/v1/devices/register                     [open]
    body {app, did, pairingToken?, deviceName?}
    no token  → GetDevice first: unknown (app,did) ⇒ 404 device_not_registered
    with token→ ValidatePairingToken(token, app): expired/invalid ⇒ 403 pairing_token_invalid
                RegisterDeviceWithPairing (D1+D7): unknown ⇒ create (owner=token user,
                name=deviceName ?? "platform·did[:12]"); known ⇒ reuse — owner only filled if
                empty, name never overwritten, refresh OS/SDK/version/platform/LastSeenAt
    → returns DeviceView + ServerConfig (idle heartbeat interval 5s)

Pairing token issue
  POST /api/v1/pairing-tokens {app}                 [requireAuth]
    → allowedApps check → token TTL 10min (pairingTokenTTL), owner = current user
    → returns {token, app, expiresAt}; same token reusable for multiple devices
    → hourly janitor purges expired tokens (aligned with health check)

SDK heartbeat (keep-alive + session-state channel)
  POST /api/v1/devices/{app}/{did}/heartbeat        [open]
    → Heartbeat: refresh LastSeenAt, find active session
    → RuleVersion (rulesVersion for SDK incremental pull)
    → returns session (nil ⇒ idle; non-nil ⇒ capturing) + dynamic interval (3s/5s)
```

### 2. Capture session lifecycle (core mechanism)

```
Activate   POST /api/v1/sessions {app,did}          [requireAuth + ownership]
             → device must exist; offline device ⇒ 409 device_offline
             → at most one capturing session per device (idempotent reuse)
             → CaptureManager.ActivateSession (session ID = ULID)

Viewer     POST /api/v1/sessions/{id}/viewers {viewerId}   [requireAuth]
             → page-level lease (TTL 120s); renew refreshes TTL; count persisted
           DELETE /api/v1/sessions/{id}/viewers/{viewerId} [requireAuth]
             → last viewer released while capturing ⇒ session ends

End        DELETE /api/v1/sessions/{id}             [requireAuth]
             → EndSession (idempotent):
                1) delete viewer leases
                2) move session traffic → retained store (7d, shareable by ID)
                3) DELETE the session record (delete-on-end)
                4) disableDeviceRules (rules persist but Enabled=false; version bump)

Timeout    background health check (every HeartbeatTimeout/2)
             → device LastSeenAt older than timeout ⇒ EndSession
             → expired viewer leases garbage-collected; last-expired ⇒ EndSession
```

### 3. Traffic pipeline

```
Upload    POST /api/v1/traffic {app,did,sessionId,entries ≤500}   [open]
            → session must be capturing (unknown ⇒ 404 session_not_found;
              ended ⇒ 409 session_ended; (app,did) mismatch ⇒ 404, no leak)
            → drop entries with empty method/url or zero timestamp (partial accept)
            → server generates IDs (ULID) + SessionID; append in-memory
            → RequestCount persisted; hit rules' LastUsedAt refreshed (sliding window)
            → 202 {accepted, count}

Query     GET /api/v1/sessions/{id}/traffic?limit&offset        [requireAuth]
            → active session: entries in arrival order (default limit 100, ≤500)
            → ended session: [] + total 0 (record is gone)
          GET /api/v1/traffic/{id}                              [requireAuth]
            → live session OR retained store of ended session
          Machine channels (v0.12.0):
            ?projection=compact → CompactTrafficView slim view (8 fields, no
              headers/bodies/query; each entry carries seq — a per-session arrival
              sequence number) — low-bandwidth pull for MCP/CLI
            ?since=N → return only entries with seq > N (incremental cursor; IDs are
              ULIDs, not strictly monotonic lexicographically within the same
              millisecond, hence the dedicated seq; since filters before paging)
            ?method=GET&scheme=https → filtering (method case-insensitive; scheme
              matched by URL prefix)
            (filters AND-combine with the existing keyword/statusCode/from/to)
            (without projection the response shape is identical to v0.11.0 — no Web UI regression)

Delete    DELETE /api/v1/traffic/{id}                           [requireAuth]
            → per-row delete; RequestCount decremented best-effort
          DELETE /api/v1/sessions/{id}/traffic                  [requireAuth]
            → clear page log; RequestCount = 0; ended session rejected

Retained  kept in-memory for RetainedTrafficTTL (7d); reachable only by ID;
          janitor purges hourly; never listed again after session end
```

### 4. Mock rules (core mechanism)

```
Create    POST /api/v1/devices/{app}/{did}/mock-rules {method,path,response,enabled,note,source?}  [requireAuth]
            → enabled=true and another enabled rule on same (Method,Path) ⇒ 409 rule_conflict
            → ID = ULID; LastUsedAt = now; BumpRuleVersion
Update    PUT  .../mock-rules/{ruleId} {response,note,enabled?} [requireAuth]
            → match key (Method+Path) and source snapshot are IMMUTABLE
            → editing the canned response with blank note ⇒ 400 (ErrNoteRequired)
            → pure toggle may leave note blank
            → turning on conflicts ⇒ 409; bump version
Delete    DELETE .../mock-rules/{ruleId}                        [requireAuth]
            → scoped to (app,did); bump version

Evaluate  every read recomputes:
            Effective = enabled && sole-enabled-on-interface
            multi-enabled interface ⇒ no rule on it is Effective ⇒ NOT mocked,
            reported as conflict (fixed message 不允许同一个接口同时开启多个 Mock 规则 —
            "multiple mock rules cannot be enabled for the same interface at once")

SDK pull  GET .../mock-rules?sinceVersion=N  [open; Web full-list branch needs auth]
            → version == sinceVersion ⇒ {version, rules: []} (no change)
            → else ⇒ only Effective rules + new version
            (the SDK applies only Effective rules)

Session end  → disableDeviceRules: all enabled rules of that device flip off;
               rules stay (re-enable manually next session) — decision #13

Janitor   hourly: purge rules whose last use (LastUsedAt→UpdatedAt→CreatedAt)
            is older than retention (7d); bumps version per device
```

### 5. Auth & ownership

```
Register  POST /api/v1/auth/register {username,password}  [open]
            → validates username/password; creates role=dev ONLY
Login     POST /api/v1/auth/login      → verify PBKD; issue AuthSession
            (token = 32 random bytes hex, TTL 7d, persisted server-side)
Logout    POST /api/v1/auth/logout     → delete session server-side (revocation)
Me        GET  /api/v1/auth/me         → current user (page refresh)

  Long-lived API Keys (v0.12.0, machine-native credentials):
Create    POST /api/v1/auth/keys                     [requireAuth]
            → issues an mnpk_-prefixed key for the current user; plaintext returned
              only once, in the create response
            → store persists only the SHA-256 hash; logs (access logs) never carry
              Authorization/key plaintext
List      GET  /api/v1/auth/keys                     [requireAuth]
            → current user's key list (id/keyPrefix/createdAt/expiresAt; no plaintext, no hash)
Revoke    DELETE /api/v1/auth/keys/{id}              [requireAuth + owner/admin]
            → revoke = immediately invalid; non-owner/non-admin gets a uniform 404
              (no key-existence leak)
Auth      authenticate() dual recognition: session token and mnpk_-prefixed API Keys
            split by prefix; an API Key carries its owning user's role, and 401/403
            semantics are identical to session tokens

Middleware requireAuth: Bearer token|API Key → UserCtx; invalid/expired ⇒ 401
          (skipped when --no-auth, smoke only)
Ownership authorizeDeviceAccess: admin OK; dev requires device.owner == username;
          otherwise 404 (no existence leak)
          authorizeSessionAccess: resolve session → owning device → same check
Admin creation: CLI only — mockd start --create-admin <user> --admin-password <pass>
```

### 5.1 Machine channels

The MCP Server and CLI are two consumers of the same contract, sharing the `pkg/mnpapi` client:

```
MCP (cmd/mocknetpack-mcp, HTTP gateway `--http-addr :PORT`, per-key Bearer passthrough validation, 12 tools)   CLI (cmd/mocknetpack, 10 leaf commands)
  list_devices            ───────────────▶ devices list
  get_device_traffic      ───────────────▶ traffic list --app --did [--since --method --scheme --compact]
  get_traffic             ───────────────▶ traffic get <id>
  create_mock_rule_from_traffic ─────────▶ rule create-from-traffic <id> --app --did --note
  create_mock_rule        ───────────────▶ rule create --app --did --method --path ...
  create_share            ───────────────▶ share create <trafficId>
  get_share               ───────────────▶ share get <shareId>
  set_mock_rule_enabled   ───────────────▶ rule set-enabled <ruleId> --enable|--disable
  update_mock_rule        ───────────────▶ rule update <ruleId> ... (supports clearBodyBase64)
  list_mock_rules         ───────────────▶ rule list --app --did [--path]
  get_mock_rule           ───────────────▶ rule get <ruleId>
  delete_mock_rule        ───────────────▶ rule delete <ruleId>

Shared conventions:
  - Auth: Authorization: Bearer <MOCKNETPACK_API_KEY> (long-lived API Key)
  - Traffic pulls default to projection=compact + since incremental cursor (low bandwidth, resumable)
  - Error responses carry "guidance for the AI": 401→configure an API Key; 404/409/403→cause + next step
  - CLI: stdout pure JSON, errors to stderr, exit codes 0 success / 1 business error / 2 usage error;
    traffic export -o writes a JSON array to disk for jq/grep post-processing
```

### 6. Share links

```
Create    POST /api/v1/shares {trafficId}         [requireAuth + device ownership]
            → snapshot = deep copy (JSON round-trip) of the traffic entry
            → SessionID cleared (no internal linkage leak)
            → ShareID = UUID; TTL 7d; URL /mocknetpack/#/share/{ShareID}
View      GET  /api/v1/shares/{id}                [public, no auth]
            → read-only snapshot; unknown/expired ⇒ 404 (expired lazily purged)
```

## End-to-end data flows

```
① Activate capture  Web: POST /sessions ──▶ server creates session
② Session awareness SDK heartbeat ──▶ server returns session!=null
③ Rule sync         SDK heartbeat returns rulesVersion → GET mock-rules?sinceVersion
                     ──▶ SDK stores Effective rules locally
④ Traffic upload    App request → SDK URLProtocol → batch → POST /traffic
                     ──▶ server in-memory per-session store (RequestCount++)
⑤ Visible in Web    Web polls GET /sessions/{id}/traffic every 2s ──▶ render
⑥ Mock hit          App request → SDK matches local rule → serves locally,
                     uploads TrafficEntry(mocked=true) → server refreshes LastUsedAt
⑦ Disconnect/out    Web DELETE /sessions/{id} (or heartbeat timeout / last viewer out)
                     ──▶ session record deleted; traffic → retained (7d);
                         rules disabled; version bump → SDK drops rules on next pull
⑧ Share             Web POST /shares{trafficId} → public GET /shares/{id} (7d, no auth)
```

## Background jobs (CaptureManager health check)

| Job | Cadence | Action |
|---|---|---|
| Device health | `HeartbeatTimeout/2` (30s) | End sessions of devices whose heartbeat timed out |
| Viewer lease GC | same ticker | Remove expired leases; end sessions whose last lease expired |
| Rule janitor | hourly | Purge rules unused > retention; bump versions |
| Retained traffic janitor | hourly | Drop retained sessions older than `RetainedTrafficTTL` |

## Error mapping (store → HTTP)

| Store error | HTTP | Code |
|---|---|---|
| `ErrDeviceNotRegistered` | 404 | `device_not_registered` |
| `ErrDeviceOffline` | 409 | `device_offline` |
| `ErrSessionNotFound` | 404 | `session_not_found` |
| `ErrSessionEnded` | 409 | `session_ended` |
| `ErrRuleNotFound` | 404 | `rule_not_found` |
| `ErrRuleConflict` | 409 | `rule_conflict` |
| `ErrNoteRequired` | 400 | `missing_field` |
| `ErrAlreadyExists` | 409 | `device_taken` / `already_exists` |
| `ErrNotFound` | 404 | `not_found` |

## Key interfaces (few, intentionally)

```go
// store.DeviceStore / CaptureSessionStore / MockRuleStore / UserStore / AuthSessionStore
//   (pkg/store/interfaces.go) — the persistence contract; implemented by pkg/store/file.

// CaptureManager (pkg/store/*_registry.go) — the domain core:
CreateManualDevice, RegisterDevice, Heartbeat, ListDevices, GetDevice, UpdateDeviceName, DeleteDevice
ActivateSession, EndSession, ListSessions, GetSession, RegisterViewer, ReleaseViewer
UploadTraffic, ListSessionTraffic, GetTraffic, GetTrafficWithOwner, DeleteTraffic, ClearSessionTraffic
CreateMockRule, UpdateMockRule, DeleteMockRule, ListMockRules, ListActiveMockRules, RuleVersion
PurgeExpiredRules, PurgeExpiredRetainedTraffic, CreateShare, GetShare
```

The HTTP layer (`pkg/admin`) is a thin translation of these; the contract schemas in `openapi/mocknetpack.yaml` are the wire truth.
