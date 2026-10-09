# MockNetPack Server — Code Map (L3)

> KB layer L3. Locate the file, then read it. "custom" = MockNetPack code; "upstream" = mockd baseline (rarely touched).

## Repo layout (top level of `server/`)

| Path | Role | Origin |
|---|---|---|
| `cmd/mockd/` | Binary entry (`main.go` → `cli.Execute()`; `mcp.go`) | upstream |
| `pkg/cli/` | Cobra CLI; `start.go` wires MockNetPack options + flags | upstream **modified** |
| `pkg/admin/` | Admin API: handlers, middleware, routes | upstream + **custom files** (below) |
| `pkg/capture/` | MockNetPack model layer | **custom** |
| `pkg/store/` | Store interfaces + `CaptureManager` | **custom** (reuses upstream storage infra) |
| `pkg/store/file/` | File-backed store implementations | **custom** |
| `pkg/account/` | Account/auth model & primitives | **custom** |
| `pkg/engine/`, `pkg/matching/`, `pkg/mcp/`, `pkg/proxy/`, `pkg/recording/`, `pkg/requestlog/`, `pkg/graphql/`, `pkg/mqtt/`, `pkg/websocket/`, `pkg/sse/`, `pkg/chaos/`, `pkg/stateful/`, `pkg/tunnel/`, `pkg/soap/`, `pkg/portability/`, `internal/*` … | mockd features (matching, store, engine, protocols) | upstream |
| `web/mocknetpack/` | Web UI (see [web KB](../web/knowledgebase/overview.md)) | **custom** |
| `openapi/mocknetpack.yaml` | `/api/v1` contract v0.8.0 (adds `/pairing-tokens`, register `pairingToken`/`deviceName`, `403 pairing_token_invalid`) | **custom** |
| `docs/` | Upstream mockd docs site (Astro) | upstream |
| `tests/`, `benchmarks/`, `charts/`, `observability/`, `schema/`, `contrib/`, `bin/` | Upstream auxiliary material | upstream |
| `README.md`, `ARCHITECTURE.md`, `CLAUDE.md`, `CHANGELOG.md`, `LICENSE`, `NOTICE`, `SECURITY.md`, `MAINTAINERS.md`, `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md` | Upstream docs/license | upstream (README head documents the fork) |
| `server.json`, `start.sh`, `stop.sh`, `install.sh`, `mise.toml`, `docker-compose*.yml`, `Dockerfile*`, `glama.json` | Run/packaging scripts | project-local helpers |

## Custom code — file level

### `pkg/capture/`

| File | Contents |
|---|---|
| `types.go` | `Platform`, `DeviceStatus`, `Device`, `DeviceView`, `DeriveDeviceStatus`, `SessionStatus`, `CaptureSession`, `ViewerLease`, `TrafficEntry` (adds `Seq` — a per-session arrival sequence number serving as the incremental cursor; not consumed by the SDK), `ServerConfig`, `MockResponse`, `MockRuleSource`, `MockRule`, `MockRuleInput`, `UpdateMockRuleInput`, `MockRuleView`, `MockRuleConflict` |
| `doc.go` | Package doc |

### `pkg/store/`

| File | Contents |
|---|---|
| `capture_registry.go` | Core shell: `CaptureConfig` + `DefaultCaptureConfig`, `CaptureManager` struct + `NewCaptureManager` (+ `pairingTokens` store), `SetLogger`/`Config`/`ServerConfig`, store error vars (`ErrPairingTokenInvalid`), `MockRuleConflictMessage`, `CreatePairingToken`/`ValidatePairingToken`/`PurgeExpiredPairingTokens` |
| `device_registry.go` | Device lifecycle: `CreateManualDevice`, `RegisterDevice`, `RegisterDeviceWithPairing` (D7: token auto-create, idempotent reuse, owner fill-if-empty, name never overwritten, `defaultDeviceName`), `Heartbeat`, `ListDevices`, `GetDevice`, `UpdateDeviceName`, `DeleteDevice` |
| `session_registry.go` | Session lifecycle + viewer leases + health check: `ActivateSession`, `EndSession`, `ListSessions`, `GetSession`, `RegisterViewer`/`ReleaseViewer`, `StartHealthCheck`/`Stop` (hourly janitor includes pairing-token purge + traffic-dir size watermark), `RestoreTraffic` (startup: loads per-session archives back into memory, drops orphan/expired files), `activeSessionFor`, `activeStatus` |
| `traffic_registry.go` | Session-scoped traffic + retained store: `UploadTraffic` (stamps the per-session seq under lock; **persists the session archive after each mutation**), `ListSessionTraffic` (extended filtering: method/scheme/since AND-combined with the existing keyword/status/from/to; `SchemeOf` parses the URL prefix), `GetTraffic(WithOwner)`, `DeleteTraffic`, `ClearSessionTraffic` (drops the archive file), `PurgeExpiredRetainedTraffic`, `retainedSession` |
| `rule_registry.go` | Mock rules: CRUD, `evaluateRules` (Effective/conflict), `RuleVersion`, `ListActiveMockRules`, `PurgeExpiredRules`, `disableDeviceRules`, `interfaceKey` |
| `share_registry.go` | Request share snapshots: `ShareTTL`, `ShareSnapshot`, `CreateShare`, `GetShare` |
| `interfaces.go` | `DeviceStore`, `CaptureSessionStore`, `MockRuleStore`, `UserStore`, `AuthSessionStore`, `PairingTokenStore`, `APIKeyStore`, `TrafficStore` (+ upstream interfaces) |
| `store.go` | Upstream store plumbing (errors, helpers) — shared with upstream |
| `engine_registry.go` | Upstream engine registry (baseline, rarely touched) |

### `pkg/store/file/`

| File | Contents |
|---|---|
| `store.go` | File-backed `Store` core (data dir, file layout) |
| `capture_store.go` | `CaptureFileStore`: devices + capture sessions persistence |
| `traffic_store.go` | `trafficStore` (`store.TrafficStore`): per-session request archives, **one file per session `traffic/<sid>.json`**, atomic tmp+fsync+rename; `SaveSessionTraffic` (empty slice removes), `DeleteSessionTraffic` (idempotent), `LoadAllSessionTraffic` (startup restore, skips corrupt/tmp) |
| `mock_rule_store.go` | `MockRuleFileStore`: rules + rule version (per app/did) |
| `pairing_token_store.go` | `PairingTokenFileStore`: pairing tokens |
| `api_key_store.go` | `apiKeyStore`: API Keys persistence (`FileData.APIKeys` written to `data.json`); `GetByHash`/`ListByUsername`/`Delete`/`DeleteExpired`; stores only the SHA-256 hash, plaintext never hits disk |
| `account_store.go` | `AccountFileStore`: users + auth sessions |
| `mock_store.go`, `other.go`, `workspaces.go`, `stateful_resource_store.go`, `custom_operation_store.go` | Upstream stores |

### `pkg/account/`

| File | Contents |
|---|---|
| `account.go` | `Role` (admin/dev), `User`, `AuthSession`, `HashPassword`/`VerifyPassword` (PBKD-SHA256), `NewToken` |
| `apikey.go` | `APIKey` model + `NewAPIKey` (plaintext exists only once at construction; `mnpk_` prefix routing) / `HashAPIKey` (SHA-256 hex) / `VerifyAPIKey` (constant-time comparison) |
| `pairing.go` | `PairingToken{Token,User,App,CreatedAt,ExpiresAt}` + `Valid(now)` |
| `account_test.go` | Tests |

### `pkg/admin/` — MockNetPack files

| File | Contents |
|---|---|
| `capture_handlers.go` | Shared contract schemas + helpers: `captureAPIPrefix = "/api/v1"`, all request/response types, `allowedApps`, `queryInt`, `writeCaptureError` |
| `device_handlers.go` | Device API: `handleCreateDevice`, `handleRegisterDevice` (+ pairingToken/deviceName branch → `RegisterDeviceWithPairing`), `handleDeviceHeartbeat` (+ `idleHeartbeatConfig`/`capturingHeartbeatConfig`/`heartbeatConfigForSession`), `handleListDevices`, `handleGetDevice`, `handleUpdateDeviceName`, `handleDeleteDevice` |
| `traffic_handlers.go` | Session/traffic/share API: `handleActivateSession`, `handleListSessions`, `handleGetSession`, `handleEndSession`, `handleRegisterViewer`/`handleReleaseViewer`, `handleUploadTraffic`, `handleGetTraffic`, `handleListSessionTraffic`, `handleDeleteTraffic`, `handleClearSessionTraffic`, `handleCreateShare`/`handleGetShare` |
| `mock_rule_handlers.go` | Rule CRUD handlers + `validateMockRuleInput` |
| `auth_handlers.go` | `handleAuthRegister/Login/Logout/Me`, `CreateAdminUser`, `bearerToken`, `validateCredentials`; logout is idempotent 204 for API Keys |
| `api_key_handlers.go` | API Key API: `handleCreateAPIKey` (POST /auth/keys, plaintext returned only in this response's `CreateAPIKeyResponse`) / `handleListAPIKeys` (GET, `ListAPIKeysResponse` contains no plaintext) / `handleDeleteAPIKey` (DELETE /auth/keys/{id}; non-owner/non-admin gets a uniform 404 — no existence leak) |
| `pairing_handlers.go` | pairing-token API: `handleCreatePairingToken` (POST /pairing-tokens, requireAuth, `CreatePairingTokenRequest/Response`) |
- `pkg/admin/local_address.go` (v0.8.1): `GET /api/v1/local-address` — LAN-reachable origin (lanIPv4 RFC1918 preferred + requestOrigin preserves the request's Host port, 503 lan_unavailable); used by the Web localhost scenario to assemble the QR code
- `pkg/account/pairing.go` (v0.8.2): `PairingToken` gains `PairedDevices` (registers paired dids; D5 reuse accumulates); `PairingUse` type
- `pkg/admin/pairing_handlers.go` (v0.8.2): `GET /api/v1/pairing-tokens/{token}` (requireAuth) — token status + paired devices (name read live from the device table); used by the Web scan-complete polling
- `pkg/store/file/pairing_token_store.go` (v0.8.2): `RecordPairingUse` (append did, dedupe, lock + markDirty)
- `pkg/capture/types.go` / `pkg/admin/capture_handlers.go` / `pkg/admin/device_handlers.go` / `pkg/store/device_registry.go` (v0.9.0): device registration and models gain `AppName` (SDK reports CFBundleDisplayName, e.g. IntegratingApp; ≤64; both upserts refresh by metadata — D7 never renames or re-owns)
| `auth_middleware.go` | `requireAuth`, `requireRole`, `currentUser`, `ownsDevice`, `authorizeDeviceAccess`, `authorizeSessionAccess`, `isAdmin`; `authenticate` dual recognition: session token and `mnpk_` API Key prefix routing, key stored only as a hash with constant-time comparison |
| `mocknetpack_web.go` | Static serving of `web/mocknetpack` under `/mocknetpack/` |
| `routes.go` | `registerRoutes` — full route table (capture section at the bottom) |
| `api.go` | `API` struct; `NewAPI` wires `store.NewCaptureManager`, `apiKeyAuth`, CORS, health check start/stop |
| `options.go` | `WithCaptureConfig`, `WithWebDir`, `WithDataDir`, `WithAPIKey*`, `WithAllowLocalhostBypass`, … |
| `types.go`, `errors.go`, `add_to_server_errors.go`, `query_parse.go`, `optional_json.go` | Small shared helpers (mix) |
| `dashboard.go`, `dashboard_stub.go` | Upstream dashboard stubs |

### `pkg/cli/`

| File | Contents |
|---|---|
| `start.go` | `mockd start` command: binds flags (incl. `--capture-heartbeat-interval`, `--capture-heartbeat-timeout`, `--capture-rule-retention-days`, `--web-dir`, `--no-auth`, `--create-admin`, `--admin-password`, `--data-dir`); constructs `NewAPI` with custom options; `--create-admin` path (CLI-only admin creation) |

### Machine channels

| Path | Contents |
|---|---|
| `pkg/mnpapi/` | HTTP client shared by MCP and CLI (`client.go` + `types.go`): `Client{BaseURL,APIKey,HTTP}`; `GetDeviceTraffic` (two-step orchestration: GET /sessions?app&did → pick the most recent session (capturing preferred) → GET /sessions/{id}/traffic; no session returns an empty result rather than an error), `CreateMockRuleFromTraffic` (GET /traffic/{id} → POST mock-rules, source snapshot + flattened headers; the source preserves the full RequestBodyDecoded/ResponseBodyDecoded — consistent with the Web's "Mock 此请求" (Mock this request), so a binary xcp reply's decoded JSON can be expanded on the rule detail page), `CreateMockRule`/`CreateShare`/`GetShare`/`GetMockRule`/`UpdateMockRule`/`ListDevices`/`GetTraffic`; `APIError`'s `Guide()` produces "guidance for the AI" (401→configure MOCKNETPACK_API_KEY, 403→owner/admin, 404→query first to confirm the id, 409→mutual-exclusion conflict, disable the old rule first) |
| `cmd/mocknetpack-mcp/` | MCP gateway (mark3labs/mcp-go v1.1.1, HTTP remote only: `--http-addr :PORT` resident process, endpoint /mcp, the consumer only fills in the URL; no stdio dispatch): 12 tools (list_devices / get_device_traffic / get_traffic / create_mock_rule_from_traffic / create_mock_rule / create_share / get_share / set_mock_rule_enabled / update_mock_rule / list_mock_rules / get_mock_rule / delete_mock_rule), each tool description states "when to use / parameter meaning / error handling"; **auth is passthrough**: the gateway holds no keys and does no allow-list validation — `WithHTTPContextFunc` injects the request's `Authorization: Bearer <key>` into the ctx (ctxBearerKey), and each handler builds a `mnpapi.Client` from the caller's own key via `clientFromContext`; key validity and owner/admin permissions are judged by mockd per account (the gateway only does format checks; missing header ⇒ 401); update and set_mock_rule_enabled both do read-modify-write (the server PUT is a full overwrite, so a pure enable/disable must echo the complete response, keeping the binary bodyBase64; update also supports clearBodyBase64=true to explicitly clear the binary body and output responseMode=text\|binary); delete_mock_rule goes through `mnpapi.DeleteMockRule` (DELETE 204) |
| `cmd/mocknetpack/` + `pkg/mnpcli/` | CLI (10 leaf commands: devices list / traffic list·export·get / share create·get / rule create-from-traffic·create·set-enabled·update); stdout pure JSON, errors to stderr, exit codes 0/1/2; `--server`/`--api-key` or `MOCKNETPACK_API_KEY`; `traffic export -o` writes a JSON array to disk for jq/grep |

## `/api/v1` route table (contract v0.12.0)

| Route | Method | Handler | Auth |
|---|---|---|---|
| `/devices/register` | POST | `handleRegisterDevice` | open (SDK) |
| `/devices/{app}/{did}/heartbeat` | POST | `handleDeviceHeartbeat` | open (SDK) |
| `/devices` | GET | `handleListDevices` | Bearer (+owner filter) |
| `/devices` | POST | `handleCreateDevice` | Bearer |
| `/devices/{app}/{did}` | GET | `handleGetDevice` | Bearer + ownership |
| `/devices/{app}/{did}` | PUT | `handleUpdateDeviceName` | Bearer + ownership |
| `/devices/{app}/{did}` | DELETE | `handleDeleteDevice` | Bearer + ownership |
| `/sessions` | POST | `handleActivateSession` | Bearer + ownership |
| `/sessions` | GET | `handleListSessions` | Bearer + ownership |
| `/sessions/{id}` | GET | `handleGetSession` | Bearer + ownership |
| `/sessions/{id}` | DELETE | `handleEndSession` | Bearer + ownership |
| `/sessions/{id}/viewers` | POST | `handleRegisterViewer` | Bearer + ownership |
| `/sessions/{id}/viewers/{viewerId}` | DELETE | `handleReleaseViewer` | Bearer + ownership |
| `/traffic` | POST | `handleUploadTraffic` | open (SDK) |
| `/traffic/{id}` | GET | `handleGetTraffic` | Bearer + ownership |
| `/sessions/{id}/traffic` | GET | `handleListSessionTraffic` | Bearer + ownership (supports projection=compact / since / method / scheme) |
| `/traffic/{id}` | DELETE | `handleDeleteTraffic` | Bearer + ownership |
| `/sessions/{id}/traffic` | DELETE | `handleClearSessionTraffic` | Bearer + ownership |
| `/devices/{app}/{did}/mock-rules` | GET | `handleListMockRules` | open pull / Bearer full-list (handler-enforced) |
| `/devices/{app}/{did}/mock-rules` | POST | `handleCreateMockRule` | Bearer + ownership |
| `/devices/{app}/{did}/mock-rules/{ruleId}` | PUT | `handleUpdateMockRule` | Bearer + ownership |
| `/devices/{app}/{did}/mock-rules/{ruleId}` | DELETE | `handleDeleteMockRule` | Bearer + ownership |
| `/auth/register` | POST | `handleAuthRegister` | open |
| `/auth/login` | POST | `handleAuthLogin` | open |
| `/auth/logout` | POST | `handleAuthLogout` | Bearer |
| `/auth/me` | GET | `handleAuthMe` | Bearer |
| `/auth/keys` | POST | `handleCreateAPIKey` | Bearer |
| `/auth/keys` | GET | `handleListAPIKeys` | Bearer |
| `/auth/keys/{id}` | DELETE | `handleDeleteAPIKey` | Bearer + owner/admin (non-owner/non-admin 404) |
| `/shares` | POST | `handleCreateShare` | Bearer + ownership |
| `/shares/{id}` | GET | `handleGetShare` | public |

## Upstream packages (one-line map, rarely touched)

| Package | What it does |
|---|---|
| `pkg/admin/engine_handlers.go`, `mocks_handlers.go`, `handlers.go` … | mockd native CRUD (mocks, workspaces, folders, engines, tunnels, proxy, recordings, chaos, state, SSE/WS/MQTT/gRPC/Soap management) |
| `pkg/engine` | Engine client/registry (data plane coordination) |
| `pkg/matching` | Request matching (upstream matchers: path/query/header/body/jsonpath) |
| `internal/storage`, `internal/matching`, `internal/runtime`, `internal/id` | Upstream internals (`internal/id` provides `ULID()`/`UUID()` used by capture) |
| `pkg/mcp` | MCP server (18 tools) |
| `pkg/proxy` | Proxy/recording manager (upstream capture channel — not used by MockNetPack ingestion) |
| `pkg/requestlog` | Upstream request log model (`TrafficEntry` follows its shape) |
| `pkg/chaos`, `pkg/stateful`, `pkg/ratelimit`, `pkg/tls`/`mtls`, `pkg/oauth`, `pkg/template`, `pkg/cli/*` | Feature areas (enable via flags) |

## CLI start flags (MockNetPack-relevant)

| Flag | Default | Meaning |
|---|---|---|
| `--port` / `--admin-port` | 4280 / 4290 | Engine / Admin ports |
| `--data-dir` | `~/.local/share/mockd` | Persistent store location |
| `--no-auth` | false | Bypass API-key + account auth (smoke only) |
| `--create-admin` / `--admin-password` | — | CLI-only admin creation |
| `--capture-heartbeat-interval` | 0 (=20s) | SDK heartbeat interval (seconds) |
| `--capture-heartbeat-timeout` | 0 (=60s) | Offline threshold (seconds) |
| `--capture-rule-retention-days` | 0 (=7d) | Mock rule sliding retention |
| `--web-dir` | `web/mocknetpack` | Web UI static directory |

## Tests

| Location | Covers |
|---|---|
| `pkg/admin/capture_handlers_test.go`, `session_handlers_test.go`, `mock_rule_handlers_test.go`, `sdk_register_test.go`, `device_manual_test.go`, `ownership_test.go`, `auth_handlers_test.go`, `auth_middleware_test.go`, `mocknetpack_web_test.go` | Custom handlers/auth/ownership |
| `pkg/store/capture_registry_test.go` (file), `capture_store_test.go`, `mock_rule_store_test.go`, `account_store_test.go` | Persistence + domain semantics |
| `pkg/capture/types_test.go` | Model helpers (`DeriveDeviceStatus` etc.) |
| `pkg/account/account_test.go` | Hashing/token |

## Where to change things (common tasks)

| Task | Touch |
|---|---|
| Change capture config defaults | `pkg/store/capture_registry.go` (`DefaultCaptureConfig`) + `pkg/cli/start.go` flags |
| Add a capture API endpoint | contract `openapi/mocknetpack.yaml` → handler in `pkg/admin/{device,traffic}_handlers.go` (or `auth_handlers.go`/`mock_rule_handlers.go` by domain) → route in `routes.go` → domain method in the matching `pkg/store/*_registry.go` |
| Change session/traffic semantics | `pkg/store/session_registry.go` (EndSession) / `traffic_registry.go` (UploadTraffic, retained store) |
| Change mock rule semantics/conflict | `pkg/store/rule_registry.go` + `pkg/capture/types.go` schemas |
| Change auth/ownership | `pkg/admin/auth_handlers.go` + `auth_middleware.go` + `pkg/account/account.go` |
| Change persistence format | `pkg/store/file/*.go` |
| Change app catalog | `allowedApps` in `pkg/admin/capture_handlers.go` |
| Web UI | `server/web/mocknetpack/` (see web KB) |
