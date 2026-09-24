# MockNetPack Web — Overview (L1)

> KB layer L1. Next: [`architecture.md`](architecture.md) → [`code-map.md`](code-map.md).
> Conceptually separate from the server, but the code lives in the same repo: `server/web/mocknetpack/` (served by the server at `/mocknetpack/`).

## What the web is

The **Web management platform** of MockNetPack: the browser UI where developers register devices, activate capture sessions, watch live request streams, and create/edit/enable per-device mock rules. It is a **framework-less single-page app** — plain `index.html` + `app.js` + `style.css`, with a vendored **CodeMirror 5.65.18** for the rule editor's JSON folding. It talks to the server's `/api/v1` capture API, which is **same-origin** (`/mocknetpack/` UI + `/api/v1` API are both served from the Admin server :4290), so there is no CORS setup.

## Role in the product

| Product role | What the web does |
|---|---|
| Device management | Device list (status/name/last-seen), manual registration (App+did+name), rename, delete |
| Session control | "连接" activates a capture session; "断开" ends it; viewer leases keep the session alive across tabs |
| Traffic viewing | Live request stream (2s polling), request detail (headers/bodies/status/duration), JSON tree display, page-level string filter, per-row delete / clear log |
| Mock rule management | Rule list with effective/conflict states, one-click "Mock 此请求" from a captured response, inline edit with JSON validation, toggle (mutually exclusive per interface), delete |
| Accounts | Login/register gate (M7), role display (admin/dev), logout with server-side revocation |
| Sharing | Public read-only share links for a single request (7d, no login) |

## Key facts

| Item | Value |
|---|---|
| Location | `server/web/mocknetpack/` (served by Admin server at `GET /mocknetpack/`) |
| Stack | Vanilla HTML/CSS/JS, no framework; vendored CodeMirror in `lib/codemirror/` |
| API base | `/api/v1` (same origin as the UI) |
| Routing | Hash-based: `#/device/{app}/{did}` (detail), `#/share/{id}` (public share) |
| Polls | Device list 5s (`POLL_MS`), traffic stream 2s (`TRAFFIC_POLL_MS`), viewer renew 60s (`RENEW_MS` = half of server TTL 120s) |
| Page log | `pageLog` buffer, cap 300 entries (`LOG_CAP`); survives disconnect/reconnect merge; cleared on back/refresh/close |
| Token | `localStorage` key `mocknetpack_token`; restored via `/auth/me` on refresh |
| Auth | `Authorization: Bearer <token>` on every API call (`apiFetch`); SDK routes are exempt server-side |

## The three views

1. **Auth view** — login/register forms (shown when no valid token).
2. **App view** — device list (`listView`) and device detail (`detailView`, two columns: left = device card + mock rules + request stream; right = detail/editor).
3. **Share view** — read-only rendering of a share link (`#/share/{id}`), no login.

## See also

- [`architecture.md`](architecture.md) — views, state, data flows, API call map.
- [`code-map.md`](code-map.md) — file/function level map.
- Server KB [`../knowledgebase/overview.md`](../knowledgebase/overview.md) — the API it calls.
- Root index [`AI_KB.md`](../../../AI_KB.md).
