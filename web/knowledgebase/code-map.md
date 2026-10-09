# MockNetPack Web — Code Map (L3)

> KB layer L3. Locate the file, then read it. All UI code lives in `server/web/mocknetpack/`.

## File tree

```
server/web/mocknetpack/
├── index.html          # single page shell — all views as divs
├── json-lossless.js    # lossless JSON tokenizing/pretty-printing/tree parsing (big-integer ID precision fix; number literals keep their original text)
├── utils.js            # shared utilities ($, STATUS, time/escape/body helpers, newUuid)
├── api.js              # token storage + Authorization header, auth API calls
├── json-tree.js        # collapsible JSON tree viewer (request/response bodies)
├── devices.js          # device list, connect/disconnect, back-to-list, device ops
├── scan-connect.js     # QR scan-connect modal (pairing-token issue, QR render, countdown)
- `scan-connect.js` v0.8.1: `isLocalHostname()` detects localhost/127.0.0.1/::1/0.0.0.0 → async `scanQRServerURL()`; when matched, first `GET /api/v1/local-address` for the LAN origin, then append `/api/v1` as the QR's `u`; on failure fall back to same-origin + error hint; non-localhost uses same-origin directly
- `scan-connect.js` v0.8.2: while the QR modal is open, poll `GET /pairing-tokens/{token}` (3s, newest did wins) → auto-close the modal → device-naming modal (required) → PUT rename → jump to `#/device/{app}/{did}`; closing the modal or refreshing the token stops the poll
- `index.html`/`app.js` (v0.8.2): `#deviceNameModal` (device-connected naming box) + button/Enter wiring
├── traffic.js          # detail view, live request-stream polling, page-log merge/filter
├── rules.js            # mock rule list/detail/editor (CodeMirror), save/delete/toggle
├── viewer.js           # viewer lease register/renew/release
├── share.js            # share-link creation + public share view
├── app.js              # shell: constants, state, hash routing, auth views, boot, wiring
├── style.css           # all styles
├── lib/codemirror/     # vendored CodeMirror 5.65.18 (CSS + JS + addons, same-origin)
└── tests/precision.test.js  # node regression: 19-digit integer ID precision (edit prefill/save/reload/tree rendering)
```

> The JS is split into classic scripts sharing one global scope. **Load order matters**
> (see `index.html`): `json-lossless.js` → `utils.js` → `api.js` → `json-tree.js` →
> `devices.js` → `traffic.js` → `rules.js` → `viewer.js` → `share.js` → `app.js`
> (boot last). `json-lossless.js` must load before `utils.js`/`json-tree.js`
> (`formatBody`/`jsonTreeHtml` depend on it). All mutable state (`viewers`, `pageLog`,
> `detail`, `trafficFilter`, …) is declared in `app.js` and only accessed at runtime,
> so file order among the middle modules is free.

## `index.html` — page blocks

| Block (id / class) | Purpose |
|---|---|
| `authView` (`.auth-wrap`) | Login/register gate; `loginForm`, `registerForm`, `authError`, `switchToRegister/Login` |
| `shareView` | Public share rendering container (`#shareContent`) |
| `appWrap` | Main app shell: `backBtn`, `mainTitle`, `apiInfo`, `userInfo`, `logoutBtn`, `errorBar` |
| `listView` | Device list: `scanConnectBtn` (scan-connect), `stats`, `list`, `updated` — **v1.5: the manual-registration entry (addDeviceBtn/addDeviceModal) was removed**; scan-connect is now the only device onboarding path |
| `scanConnectModal` | Scan-connect modal: `addApp` (App dropdown, moved in from the old registration modal in v1.5), `scanConnectApp`, `scanConnectError`, `scanConnectQR`, `scanConnectCountdown`, `scanConnectRefresh`, `scanConnectCancel` |
| `detailView` | Device detail (two-column `.detail-grid`): |
| — `.col-left` | `device-card` (`dDid`, `dStatus`, `dLastSeen`, `dToggleSession`), rules section (`rulesList`, `rulesConflict`), traffic section (`trafficClearBtn`, `trafficFilter`, `trafficList`) |
| — `.col-right` | `ruleDetail`, `trafficDetail` (inline detail/editor) |
| scripts | CodeMirror css/js + addons + qrcode.min.js, then (in order) `json-lossless.js` → `utils.js` → `api.js` → `json-tree.js` → `devices.js` → `scan-connect.js` → `traffic.js` → `rules.js` → `viewer.js` → `share.js` → `app.js` |

## JS modules — key functions

| File | Key functions |
|---|---|
| `json-lossless.js` | `jsonTokens`, `jsonParseTree`, `jsonPrettyPrint` (strict JSON tokenizing + lossless tree parse/pretty-print; number literals preserved verbatim for edit prefill and detail/share display; fixes 19-digit integer ID precision) |
| `utils.js` | `$`, `STATUS`, `showError`, `relTime`, `clockTime`, `esc`, `formatBody` (losslessly pretty-printed via `jsonPrettyPrint`), `shortId`, `methodCls`, `newUuid` |
| `api.js` | `getToken` / `setToken`, `apiFetch` (adds Bearer header), `doLogin`, `doRegister`, `doLogout` |
| `json-tree.js` | `JSON_TREE_MAX_NODES`, `jsonTreeHtml`, `jvCount`, `jvNode`, `bodyHtml`, `bindJsonTree` |
| `devices.js` | `loadDevices`, `render` (status order, empty state, **three-line card: name / full DID / app-badge + status + relTime; since v0.9.0 the app-badge prefers `appName` (e.g. IntegratingApp) and falls back to the bundle id**), `openDetail`, `connect`, `disconnect`, `deleteDevice`, `renameDevice` |
| `scan-connect.js` | (v0.8.2) `base64url`, `isLocalHostname`, `scanQRServerURL` (async, LAN origin), `scanQRText`, `openScanConnectModal`, `issueScanToken`, `renderScanQR`, `startScanCountdown`/`clearScanCountdown`, `closeScanConnectModal`, `startScanPoll`/`stopScanPoll` (3s token-status poll), `openDeviceNameModal`/`closeDeviceNameModal`/`submitDeviceName` (PUT rename + hash jump) |
| `traffic.js` | `openDetail`, `enterDetail`, `loadDetail`, `bindSession`, `stopTrafficPoll`, `showDetailPane`, `pollTraffic`, `mergeLog`, `renderTraffic`, `deleteTrafficEntry`, `clearTrafficLog`, `renderTrafficDetail` |
| `history.js` | (v1.4, M2 历史日志) `openHistory`/`closeHistory` (pauses live polling), `loadHistorySessions`/`selectHistorySession`/`applyHistoryFilter`/`loadHistoryPage`/`loadHistoryMore`/`renderHistoryList` (keyword/statusCode/from/to server-side filter + pagination; 行点击经 `renderHistoryDetail` 复用 `renderTrafficDetail` 在浮层右侧展开详情), `renderHistoryDetail` — read-only, no delete buttons (48h 保留语义；数据由服务端按会话落盘存档，重启不丢) |
| `rules.js` | `loadRules`, `renderRules`, `renderRuleDetail`, `openEditRuleForm` (CodeMirror), `saveRuleEdit`, `deleteRule`, `toggleRule`, `mockThisRequest` |
| `viewer.js` | `registerViewer`, `renewViewer`, `stopViewer`, `releaseAllViewers` (+ `pagehide` listener) |
| `share.js` | `renderShareView` (public), `shareRequest` |
| `app.js` | Constants (`API`, `POLL_MS`, `TRAFFIC_POLL_MS`, `TRAFFIC_PAGE`, `RENEW_MS`, `LOG_CAP`), state (`viewers`, `pollTimer`, `detail`, `ruleStore`, `pageLog`, `trafficFilter`, …), hash routing, `showAuth`/`showAuthError`/`hideAuthError`, `enterApp`, `boot`, wiring IIFEs (traffic filter input, add-device modal, scan-connect modal) |

## `style.css`

Layout for the auth card, device list cards, two-column detail grid, badges (idle/capturing/offline), conflict banner, modal, CodeMirror editor, delete buttons. Device delete button color/position was hardened with inline styles + `!important` (history).

## `lib/codemirror/`

Vendored CodeMirror 5.65.18 (same-origin; local README documents the vendor). Used only by the rule edit form for JSON folding (`foldcode`, `foldgutter`, `brace-fold`, `matchbrackets`, `closebrackets`, javascript mode).

## Where to change things (common tasks)

| Task | Touch |
|---|---|
| Add a view/block | `index.html` (div + id) + the matching module's show/hide + render function |
| Change polling cadence | `app.js` constants (`POLL_MS`, `TRAFFIC_POLL_MS`, `RENEW_MS`) |
| Change a request's API shape | the module's `apiFetch` call site + server contract `openapi/mocknetpack.yaml` + server handler |
| Change rule editor behavior | `rules.js` (`openEditRuleForm` / `saveRuleEdit` — CodeMirror init, JSON validation) |
| Change page-log semantics | `traffic.js` (`pageLog`, `mergeLog`, `renderTraffic`, `stopTrafficPoll`) + `app.js` state + `api.js` `doLogout` cleanup |
| Change auth UX | `api.js` (`doLogin`/`doRegister`/`doLogout`) + `app.js` (`showAuth`/`boot`) |
| Change share view | `share.js` (`renderShareView`) + server `GET /shares/{id}` |
| Change display rule (page filter) | `app.js` wiring (traffic filter input) + `traffic.js` `renderTraffic` |
