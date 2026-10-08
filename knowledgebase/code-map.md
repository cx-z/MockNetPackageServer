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
| `pkg/account/` | Account/auth model & primitives  | **custom** |
| `pkg/engine/`, `pkg/matching/`, `pkg/mcp/`, `pkg/proxy/`, `pkg/recording/`, `pkg/requestlog/`, `pkg/graphql/`, `pkg/mqtt/`, `pkg/websocket/`, `pkg/sse/`, `pkg/chaos/`, `pkg/stateful/`, `pkg/tunnel/`, `pkg/soap/`, `pkg/portability/`, `internal/*` … | mockd features (matching, store, engine, protocols) | upstream |
| `web/mocknetpack/` | Web UI (see [web KB](../web/knowledgebase/overview.md)) | **custom** |
| `openapi/mocknetpack.yaml` | `/api/v1` contract v0.8.0 (: `/pairing-tokens`, register `pairingToken`/`deviceName`, `403 pairing_token_invalid`) | **custom** |
| `docs/` | Upstream mockd docs site (Astro) | upstream |
| `tests/`, `benchmarks/`, `charts/`, `observability/`, `schema/`, `contrib/`, `bin/` | Upstream auxiliary material | upstream |
| `README.md`, `ARCHITECTURE.md`, `CLAUDE.md`, `CHANGELOG.md`, `LICENSE`, `NOTICE`, `SECURITY.md`, `MAINTAINERS.md`, `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md` | Upstream docs/license | upstream (README head documents the fork) |
| `server.json`, `start.sh`, `stop.sh`, `install.sh`, `mise.toml`, `docker-compose*.yml`, `Dockerfile*`, `glama.json` | Run/packaging scripts | project-local helpers |

## Custom code — file level

### `pkg/capture/`

| File | Contents |
|---|---|
| `types.go` | `Platform`, `DeviceStatus`, `Device`, `DeviceView`, `DeriveDeviceStatus`, `SessionStatus`, `CaptureSession`, `ViewerLease`, `TrafficEntry`（ 增 `Seq` 每会话自增到达序号，增量游标，SDK 不消费）, `ServerConfig`, `MockResponse`, `MockRuleSource`, `MockRule`, `MockRuleInput`, `UpdateMockRuleInput`, `MockRuleView`, `MockRuleConflict` |
| `doc.go` | Package doc |

### `pkg/store/`

| File | Contents |
|---|---|
| `capture_registry.go` | Core shell: `CaptureConfig` + `DefaultCaptureConfig`, `CaptureManager` struct + `NewCaptureManager` (+ `pairingTokens` store), `SetLogger`/`Config`/`ServerConfig`, store error vars (`ErrPairingTokenInvalid` ), `MockRuleConflictMessage`, `CreatePairingToken`/`ValidatePairingToken`/`PurgeExpiredPairingTokens` |
| `device_registry.go` | Device lifecycle: `CreateManualDevice`, `RegisterDevice`, `RegisterDeviceWithPairing` (/D7: token auto-create, idempotent reuse, owner fill-if-empty, name never overwritten, `defaultDeviceName`), `Heartbeat`, `ListDevices`, `GetDevice`, `UpdateDeviceName`, `DeleteDevice` |
| `session_registry.go` | Session lifecycle + viewer leases + health check: `ActivateSession`, `EndSession`, `ListSessions`, `GetSession`, `RegisterViewer`/`ReleaseViewer`, `StartHealthCheck`/`Stop` (hourly janitor includes pairing-token purge), `activeSessionFor`, `activeStatus` |
| `traffic_registry.go` | Session-scoped traffic + retained store: `UploadTraffic`（ 锁内 stamp 每会话自增 seq）、`ListSessionTraffic`（ 过滤扩展：method/scheme/since 与既有 keyword/status/from/to AND 组合；`SchemeOf` URL 前缀解析）、`GetTraffic(WithOwner)`, `DeleteTraffic`, `ClearSessionTraffic`, `PurgeExpiredRetainedTraffic`, `retainedSession` |
| `rule_registry.go` | Mock rules: CRUD, `evaluateRules` (Effective/conflict), `RuleVersion`, `ListActiveMockRules`, `PurgeExpiredRules`, `disableDeviceRules`, `interfaceKey` |
| `share_registry.go` | Request share snapshots : `ShareTTL`, `ShareSnapshot`, `CreateShare`, `GetShare` |
| `interfaces.go` | `DeviceStore`, `CaptureSessionStore`, `MockRuleStore`, `UserStore`, `AuthSessionStore`, `PairingTokenStore` , `APIKeyStore`  (+ upstream interfaces) |
| `store.go` | Upstream store plumbing (errors, helpers) — shared with upstream |
| `engine_registry.go` | Upstream engine registry (baseline, rarely touched) |

### `pkg/store/file/`

| File | Contents |
|---|---|
| `store.go` | File-backed `Store` core (data dir, file layout) |
| `capture_store.go` | `CaptureFileStore`: devices + capture sessions persistence |
| `mock_rule_store.go` | `MockRuleFileStore`: rules + rule version (per app/did) |
| `pairing_token_store.go` | `PairingTokenFileStore`: pairing tokens  |
| `api_key_store.go` | `apiKeyStore`: API Keys  持久化（FileData.APIKeys 落盘 data.json）；`GetByHash`/`ListByUsername`/`Delete`/`DeleteExpired`；只存 SHA-256 哈希，明文永不落盘 |
| `account_store.go` | `AccountFileStore`: users + auth sessions |
| `mock_store.go`, `other.go`, `workspaces.go`, `stateful_resource_store.go`, `custom_operation_store.go` | Upstream stores |

### `pkg/account/`

| File | Contents |
|---|---|
| `account.go` | `Role` (admin/dev), `User`, `AuthSession`, `HashPassword`/`VerifyPassword` (PBKD-SHA256), `NewToken` |
| `apikey.go` | `APIKey` 模型 + `NewAPIKey`（明文仅构造时存在一次，`mnpk_` 前缀路由）/ `HashAPIKey`（SHA-256 hex）/ `VerifyAPIKey`（恒时比较）， |
| `pairing.go` | `PairingToken{Token,User,App,CreatedAt,ExpiresAt}` + `Valid(now)`  |
| `account_test.go` | Tests |

### `pkg/admin/` — MockNetPack files

| File | Contents |
|---|---|
| `capture_handlers.go` | Shared contract schemas + helpers: `captureAPIPrefix = "/api/v1"`, all request/response types, `allowedApps`, `queryInt`, `writeCaptureError` |
| `device_handlers.go` | Device API: `handleCreateDevice`, `handleRegisterDevice` (+  pairingToken/deviceName branch → `RegisterDeviceWithPairing`), `handleDeviceHeartbeat` (+ `idleHeartbeatConfig`/`capturingHeartbeatConfig`/`heartbeatConfigForSession`), `handleListDevices`, `handleGetDevice`, `handleUpdateDeviceName`, `handleDeleteDevice` |
| `traffic_handlers.go` | Session/traffic/share API: `handleActivateSession`, `handleListSessions`, `handleGetSession`, `handleEndSession`, `handleRegisterViewer`/`handleReleaseViewer`, `handleUploadTraffic`, `handleGetTraffic`, `handleListSessionTraffic`, `handleDeleteTraffic`, `handleClearSessionTraffic`, `handleCreateShare`/`handleGetShare` |
| `mock_rule_handlers.go` | Rule CRUD handlers + `validateMockRuleInput` |
| `auth_handlers.go` | `handleAuthRegister/Login/Logout/Me`, `CreateAdminUser`, `bearerToken`, `validateCredentials`；logout 对 API Key 幂等 204 |
| `api_key_handlers.go` |  API Key API：`handleCreateAPIKey`（POST /auth/keys，明文仅本次返回 `CreateAPIKeyResponse`）/ `handleListAPIKeys`（GET，`ListAPIKeysResponse` 不含明文）/ `handleDeleteAPIKey`（DELETE /auth/keys/{id}；非 owner 非 admin 统一 404 不泄露存在性） |
| `pairing_handlers.go` |  pairing-token API: `handleCreatePairingToken` (POST /pairing-tokens, requireAuth, `CreatePairingTokenRequest/Response`) |
- `pkg/admin/local_address.go`（ v0.8.1）：`GET /api/v1/local-address`——局域网可达 origin（lanIPv4 RFC1918 优先 + requestOrigin 保留请求 Host 端口，503 lan_unavailable）；Web localhost 场景组装二维码用
- `pkg/account/pairing.go`（v0.8.2）：`PairingToken` 增 `PairedDevices`（配对注册登记 did，D5 可复用累加）；`PairingUse` 类型
- `pkg/admin/pairing_handlers.go`（v0.8.2）：`GET /api/v1/pairing-tokens/{token}`（requireAuth）——令牌状态 + 已配对设备（name 实时从设备表读）；Web 扫码完成轮询用
- `pkg/store/file/pairing_token_store.go`（v0.8.2）：`RecordPairingUse`（追加 did、去重、锁 + markDirty）
- `pkg/capture/types.go` / `pkg/admin/capture_handlers.go` / `pkg/admin/device_handlers.go` / `pkg/store/device_registry.go`（v0.9.0）：设备注册与模型增 `AppName`（SDK 上报 CFBundleDisplayName，如 IntegratingApp；≤64；两个 upsert 均按元数据刷新，D7 不改名不换 owner）
| `auth_middleware.go` | `requireAuth`, `requireRole`, `currentUser`, `ownsDevice`, `authorizeDeviceAccess`, `authorizeSessionAccess`, `isAdmin`；`authenticate` 双识别：session token 与 `mnpk_` API Key 前缀分流、key 仅存 hash 恒时比较 |
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

### 机器通道

| 路径 | 内容 |
|---|---|
| `pkg/mnpapi/` | MCP 与 CLI 共享的 HTTP 客户端（`client.go` + `types.go`）：`Client{BaseURL,APIKey,HTTP}`；`GetDeviceTraffic`（两步编排：GET /sessions?app&did → 取最近会话（capturing 优先）→ GET /sessions/{id}/traffic，无会话返回空结果而非报错）、`CreateMockRuleFromTraffic`（GET /traffic/{id} → POST mock-rules，source 快照 + 扁平化 headers； 起 source 完整保留 RequestBodyDecoded/ResponseBodyDecoded——与 Web「Mock 此请求」一致，二进制 xcp 回包可在规则详情页展开已解码 JSON）、`CreateMockRule`/`CreateShare`/`GetShare`/`GetMockRule`/`UpdateMockRule`/`ListDevices`/`GetTraffic`；`APIError` 的 `Guide()` 生成「给 AI 的指引」（401→配置 MOCKNETPACK_API_KEY、403→owner/admin、404→先查询确认 id、409→互斥冲突先停旧） |
| `cmd/mocknetpack-mcp/` | MCP 网关（mark3labs/mcp-go v1.1.1，仅 HTTP 远程形态：`--http-addr :PORT` 常驻进程，端点 /mcp，业务侧只填 URL；无 stdio 分发）：12 个 tools（list_devices / get_device_traffic / get_traffic / create_mock_rule_from_traffic / create_mock_rule / create_share / get_share / set_mock_rule_enabled / update_mock_rule / list_mock_rules / get_mock_rule / delete_mock_rule），每个 tool 描述写明「何时用/参数含义/错误处理」；**鉴权为直通模式**：网关不持有/白名单校验 Key，`WithHTTPContextFunc` 将请求 `Authorization: Bearer <key>` 注入 ctx（ctxBearerKey），各 handler 经 `clientFromContext` 用调用方自己的 key 构造 mnpapi.Client，key 有效性与 owner/admin 权限由 mockd 按账号判定（网关只做格式校验，缺头 401）；update 与 set_mock_rule_enabled 均走 read-modify-write（服务端 PUT 为整体覆盖，纯启停必须回写完整 response，二进制 bodyBase64 保留； 起 update 支持 clearBodyBase64=true 显式清二进制体并输出 responseMode=text\|binary）；delete_mock_rule 走 mnpapi.DeleteMockRule（DELETE 204） |
| `cmd/mocknetpack/` + `pkg/mnpcli/` | CLI（10 个叶子命令：devices list / traffic list·export·get / share create·get / rule create-from-traffic·create·set-enabled·update）；stdout 纯 JSON、错误走 stderr、退出码 0/1/2；`--server`/`--api-key` 或 `MOCKNETPACK_API_KEY`；`traffic export -o` 落盘 JSON 数组供 jq/grep |

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
| `/sessions/{id}/traffic` | GET | `handleListSessionTraffic` | Bearer + ownership（：projection=compact / since / method / scheme） |
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
| `/auth/keys/{id}` | DELETE | `handleDeleteAPIKey` | Bearer + owner/admin（非 owner 非 admin 404） |
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
| `--create-admin` / `--admin-password` | — | CLI-only admin creation  |
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
