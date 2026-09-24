# MockNetPack Server — Overview (L1)

> KB layer L1. Next: [`architecture.md`](architecture.md) → [`code-map.md`](code-map.md).
> Product source of truth: `tasks/需求文档.md` v1.2, contract `openapi/mocknetpack.yaml` v0.7.0.

## What the server is

The **central Mock server** of MockNetPack: a Go service (a fork of [getmockd/mockd](https://github.com/getmockd/mockd) @ v0.7.1, Apache-2.0) extended with the **MockNetPack capture platform**. It manages devices, capture sessions, traffic records and per-device mock rules; it serves the Web management UI and the `/api/v1` capture API from the **Admin server (:4290)**. The upstream mockd **Engine server (:4280)** remains for mockd's native data-plane behavior.

The server **never proxies business traffic**: it only records and manages. Requests that miss a mock go straight from the device to the real API server.

## Two servers in one binary

| Server | Port | Role in MockNetPack |
|---|---|---|
| Admin API | `:4290` | All MockNetPack routes (`/api/v1/...`), auth (`/api/v1/auth/...`), shares, upstream mockd admin API (`/mocks`, `/workspaces`, …), and the Web UI at `/mocknetpack/` (same origin) |
| Engine | `:4280` | Upstream mockd data plane (serves mocks, records, MCP, etc.). The iOS smoke tool uses it as a real request target; MockNetPack capture/mock execution happens in the SDK, not here |

## What MockNetPack adds to upstream mockd

| Package | Added for | Status |
|---|---|---|
| `pkg/capture` | Device / CaptureSession / TrafficEntry / MockRule model layer (platform-neutral) | custom |
| `pkg/store` (+ `pkg/store/file`) | `CaptureManager` runtime semantics + persistent stores (devices, sessions, rules, accounts, sessions) over the file-backed store | custom (reuses upstream `internal/storage`/store infra) |
| `pkg/account` | User + AuthSession model, PBKDF2 password hashing, session token primitives (M7) | custom |
| `pkg/admin` (subset) | `/api/v1` capture handlers, auth handlers/middleware, mock-rule handlers, share handlers, Web static serving | custom (rest of `pkg/admin` is upstream) |
| `pkg/cli/start.go` | `mockd start` wiring for capture config, `--no-auth`, `--create-admin`, `--web-dir` | modified |
| `openapi/mocknetpack.yaml` | The `/api/v1` contract (currently v0.7.0) | custom |

> How to tell custom from upstream quickly: custom code references `mocknetpack` or the `capture`/`account` packages, or appears in the git log as `M3.5`…`M8.6` commits. Upstream code is everything else (engine, mcp, matching, proxy, …).

## Core concepts

| Concept | Definition (server side) |
|---|---|
| **App** | Application identifier (e.g. `com.example.integrating`). First product dimension; a fixed catalog (`allowedApps`) — one app today |
| **Device** | A running app instance on one physical device/simulator, uniquely `(App, did)`. M7.2+: created **manually** in the Web UI (name + owner); SDK never auto-registers |
| **Device status** | Derived, not persisted: `idle` / `capturing` / `offline` (offline > capturing > idle priority) |
| **Capture session** | A capture period activated from the Web. One per device. Ends on last-viewer release, heartbeat timeout, or forced disconnect — and is **deleted on end** (M9): no history, only the current session |
| **Traffic** | A captured HTTP request/response (`TrafficEntry`). Session-scoped, in-memory; moved to a retained store on session end (shareable by ID for 7d) |
| **Mock rule** | A canned response for one interface, bound to `(App, did)`. Matches `Method + URL path`. One active rule per interface; persisted with a 7-day sliding retention |
| **Display rule** | Web-page-level string filter — ephemeral, not a server-persisted concept (handled in the web KB) |
| **Share link** | Public, read-only, 7-day snapshot of a single traffic entry (M8.5), decoupled from the session |
| **Account** | `admin` or `dev`. Open registration creates `dev` only; `admin` only via CLI `--create-admin`. Devs see only their own devices; admin sees all |

## Key facts & defaults

| Item | Value |
|---|---|
| Contract | `server/openapi/mocknetpack.yaml` **v0.7.0**, base path `/api/v1` |
| Heartbeat interval / timeout | 20s / 60s defaults; SDK gets dynamic interval (3s capturing / 5s idle) from M8.2/M8.3 |
| Viewer lease TTL | 120s (web renews at ~60s) |
| Mock rule retention | 7 days sliding (since last use); janitor hourly |
| Retained traffic window | 7 days (`RetainedTrafficTTL`, aligned with share TTL) |
| Share link TTL | 7 days |
| Traffic upload | batches ≤ 500 entries; session must be capturing; partial acceptance allowed |
| App catalog | `com.example.integrating` (fixed; admin-only app management is backlog) |
| CLI | `mockd start --port 4280 --admin-port 4290`; capture flags `--capture-heartbeat-interval`, `--capture-heartbeat-timeout`, `--capture-rule-retention-days`, `--web-dir`; auth flags `--no-auth`, `--create-admin <user> --admin-password <pass>` |

## Auth model (M7)

- **Open registration** (`POST /api/v1/auth/register`) → always creates role `dev`.
- **Login** (`POST /api/v1/auth/login`) → server-issued session token (32 random bytes hex, 7d TTL, server-side revocation).
- **Logout** revokes server-side; `GET /auth/me` restores the session on page refresh.
- **Middleware**: Web-facing `/api/v1` routes require `Authorization: Bearer <token>` (`requireAuth`); SDK-facing routes (register, heartbeat, traffic upload, mock-rules pull) stay **open** (the SDK carries no credentials; the did is its identity). `--no-auth` bypasses for local smoke only.
- **Ownership**: devs see/operate only devices with `owner == username`; admins everything. Unknown device ⇒ 404 (no existence leak).

## See also

- [`architecture.md`](architecture.md) — domain flows and data flows (detailed).
- [`code-map.md`](code-map.md) — package/file map, route table, upstream map.
- Web end: [`../web/knowledgebase/overview.md`](../web/knowledgebase/overview.md) (same repo).
- Root index [`AI_KB.md`](../../AI_KB.md).
