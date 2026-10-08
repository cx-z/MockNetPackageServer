# MockNetPack Web — Code Map (L3)

> KB layer L3. Locate the file, then read it. All UI code lives in `server/web/mocknetpack/`.

## File tree

```
server/web/mocknetpack/
├── index.html          # single page shell — all views as divs
├── json-lossless.js    # 无损 JSON 分词/美化/树解析（大整数 ID 精度修复；数字保留原文 token）
├── utils.js            # shared utilities ($, STATUS, time/escape/body helpers, newUuid)
├── api.js              # token storage + Authorization header, auth API calls
├── json-tree.js        # collapsible JSON tree viewer (request/response bodies)
├── devices.js          # device list, connect/disconnect, back-to-list, device ops
├── scan-connect.js      #  QR scan-connect modal (pairing-token issue, QR render, countdown)
- `scan-connect.js` v0.8.1：`isLocalHostname()` 判定 localhost/127.0.0.1/::1/0.0.0.0 → `scanQRServerURL()` async，命中时先 `GET /api/v1/local-address` 拿 LAN origin 拼 `/api/v1` 作二维码 `u`；失败回退同源 + 错误提示；非 localhost 直接同源
- `scan-connect.js` v0.8.2：二维码弹窗打开期间轮询 `GET /pairing-tokens/{token}`（3s，新增 did 为准）→ 自动关弹窗 → 设备命名弹窗（必填）→ PUT 改名 → 跳转 `#/device/{app}/{did}`；关闭弹窗/刷新令牌停轮询
- `index.html`/`app.js`（v0.8.2）：`#deviceNameModal`（设备已连接命名框）+ 按钮/回车 wiring
├── traffic.js          # detail view, live request-stream polling, page-log merge/filter
├── rules.js            # mock rule list/detail/editor (CodeMirror), save/delete/toggle
├── viewer.js           # viewer lease register/renew/release
├── share.js            # share-link creation + public share view
├── app.js              # shell: constants, state, hash routing, auth views, boot, wiring
├── style.css           # all styles
├── lib/codemirror/     # vendored CodeMirror 5.65.18 (CSS + JS + addons, same-origin)
└── tests/precision.test.js  # node 回归：19 位整数 ID 精度（编辑预填/保存/重载/树渲染）
```

> The JS is split into classic scripts sharing one global scope. **Load order matters**
> (see `index.html`): `json-lossless.js` → `utils.js` → `api.js` → `json-tree.js` →
> `devices.js` → `traffic.js` → `rules.js` → `viewer.js` → `share.js` → `app.js`
> (boot last). `json-lossless.js` 必须在 `utils.js`/`json-tree.js` 之前加载（
> `formatBody`/`jsonTreeHtml` 依赖它）。All mutable state (`viewers`, `pageLog`,
> `detail`, `trafficFilter`, …) is declared in `app.js` and only accessed at runtime,
> so file order among the middle modules is free.

## `index.html` — page blocks

| Block (id / class) | Purpose |
|---|---|
| `authView` (`.auth-wrap`) | Login/register gate; `loginForm`, `registerForm`, `authError`, `switchToRegister/Login` |
| `shareView` | Public share rendering container (`#shareContent`) |
| `appWrap` | Main app shell: `backBtn`, `mainTitle`, `apiInfo`, `userInfo`, `logoutBtn`, `errorBar` |
| `listView` | Device list: `scanConnectBtn`（扫码连接）、`stats`、`list`、`updated` — **v1.5：手动注册入口（addDeviceBtn/addDeviceModal）已移除**，设备接入唯一入口为扫码连接 |
| `scanConnectModal` | Scan-connect modal: `addApp`（App 下拉，v1.5 由原注册弹窗迁入）、`scanConnectApp`、`scanConnectError`、`scanConnectQR`、`scanConnectCountdown`、`scanConnectRefresh`、`scanConnectCancel` |
| `detailView` | Device detail (two-column `.detail-grid`): |
| — `.col-left` | `device-card` (`dDid`, `dStatus`, `dLastSeen`, `dToggleSession`), rules section (`rulesList`, `rulesConflict`), traffic section (`trafficClearBtn`, `trafficFilter`, `trafficList`) |
| — `.col-right` | `ruleDetail`, `trafficDetail` (inline detail/editor) |
| scripts | CodeMirror css/js + addons + qrcode.min.js, then (in order) `json-lossless.js` → `utils.js` → `api.js` → `json-tree.js` → `devices.js` → `scan-connect.js`  → `traffic.js` → `rules.js` → `viewer.js` → `share.js` → `app.js` |

## JS modules — key functions

| File | Key functions |
|---|---|
| `json-lossless.js` | `jsonTokens`, `jsonParseTree`, `jsonPrettyPrint`（严格 JSON 分词 + 无损树解析/美化；数字字面量原文保留，用于编辑预填与详情/分享展示，修复 19 位整数 ID 精度） |
| `utils.js` | `$`, `STATUS`, `showError`, `relTime`, `clockTime`, `esc`, `formatBody`（经 `jsonPrettyPrint` 无损美化）, `shortId`, `methodCls`, `newUuid` |
| `api.js` | `getToken` / `setToken`, `apiFetch` (adds Bearer header), `doLogin`, `doRegister`, `doLogout` |
| `json-tree.js` | `JSON_TREE_MAX_NODES`, `jsonTreeHtml`, `jvCount`, `jvNode`, `bodyHtml`, `bindJsonTree` |
| `devices.js` | : `loadDevices`, `render` (status order, empty state, **三行式卡片：name / 完整 DID / app-badge + status + relTime；v0.9.0 app-badge 优先 `appName`（如 IntegratingApp）回退 bundle id**), `openDetail`, `connect`, `disconnect`, `deleteDevice`, `renameDevice` |
| `scan-connect.js` | /v0.8.2: `base64url`, `isLocalHostname`, `scanQRServerURL` (async, LAN origin), `scanQRText`, `openScanConnectModal`, `issueScanToken`, `renderScanQR`, `startScanCountdown`/`clearScanCountdown`, `closeScanConnectModal`, `startScanPoll`/`stopScanPoll` (3s token-status poll), `openDeviceNameModal`/`closeDeviceNameModal`/`submitDeviceName` (PUT rename + hash jump) |
| `traffic.js` | `openDetail`, `enterDetail`, `loadDetail`, `bindSession`, `stopTrafficPoll`, `showDetailPane`, `pollTraffic`, `mergeLog`, `renderTraffic`, `deleteTrafficEntry`, `clearTrafficLog`, `renderTrafficDetail` |
| `rules.js` | `loadRules`, `renderRules`, `renderRuleDetail`, `openEditRuleForm` (CodeMirror), `saveRuleEdit`, `deleteRule`, `toggleRule`, `mockThisRequest` |
| `viewer.js` | `registerViewer`, `renewViewer`, `stopViewer`, `releaseAllViewers` (+ `pagehide` listener) |
| `share.js` | `renderShareView` (public), `shareRequest` |
| `app.js` | Constants (`API`, `POLL_MS`, `TRAFFIC_POLL_MS`, `TRAFFIC_PAGE`, `RENEW_MS`, `LOG_CAP`), state (`viewers`, `pollTimer`, `detail`, `ruleStore`, `pageLog`, `trafficFilter`, …), hash routing, `showAuth`/`showAuthError`/`hideAuthError`, `enterApp`, `boot`, wiring IIFEs (traffic filter input, add-device modal, scan-connect modal ) |

## `style.css`

Layout for the auth card, device list cards, two-column detail grid, badges (idle/capturing/offline), conflict banner, modal, CodeMirror editor, delete buttons. Device delete button color/position was hardened with inline styles + `!important` ( history).

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
