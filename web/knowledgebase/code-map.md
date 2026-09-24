# MockNetPack Web — Code Map (L3)

> KB layer L3. Locate the file, then read it. All UI code lives in `server/web/mocknetpack/`.

## File tree

```
server/web/mocknetpack/
├── index.html          # single page shell — all views as divs
├── utils.js            # shared utilities ($, STATUS, time/escape/body helpers, newUuid)
├── api.js              # token storage + Authorization header, auth API calls
├── json-tree.js        # collapsible JSON tree viewer (request/response bodies)
├── devices.js          # device list, connect/disconnect, back-to-list, device ops
├── traffic.js          # detail view, live request-stream polling, page-log merge/filter
├── rules.js            # mock rule list/detail/editor (CodeMirror), save/delete/toggle
├── viewer.js           # viewer lease register/renew/release
├── share.js            # share-link creation + public share view
├── app.js              # shell: constants, state, hash routing, auth views, boot, wiring
├── style.css           # all styles
└── lib/codemirror/     # vendored CodeMirror 5.65.18 (CSS + JS + addons, same-origin)
```

> The JS is split into classic scripts sharing one global scope. **Load order matters**
> (see `index.html`): `utils.js` → `api.js` → `json-tree.js` → `devices.js` → `traffic.js`
> → `rules.js` → `viewer.js` → `share.js` → `app.js` (boot last). All mutable state
> (`viewers`, `pageLog`, `detail`, `trafficFilter`, …) is declared in `app.js` and only
> accessed at runtime, so file order among the middle modules is free.

## `index.html` — page blocks

| Block (id / class) | Purpose |
|---|---|
| `authView` (`.auth-wrap`) | Login/register gate; `loginForm`, `registerForm`, `authError`, `switchToRegister/Login` |
| `shareView` | Public share rendering container (`#shareContent`) |
| `appWrap` | Main app shell: `backBtn`, `mainTitle`, `apiInfo`, `userInfo`, `logoutBtn`, `errorBar` |
| `listView` | Device list: `addDeviceBtn`, `stats`, `list`, `updated` |
| `addDeviceModal` | Manual registration modal: `addApp` (select, fixed catalog), `addDid`, `addName` |
| `detailView` | Device detail (two-column `.detail-grid`): |
| — `.col-left` | `device-card` (`dDid`, `dStatus`, `dLastSeen`, `dToggleSession`), rules section (`rulesList`, `rulesConflict`), traffic section (`trafficClearBtn`, `trafficFilter`, `trafficList`) |
| — `.col-right` | `ruleDetail`, `trafficDetail` (inline detail/editor) |
| scripts | CodeMirror css/js + addons, then (in order) `utils.js` → `api.js` → `json-tree.js` → `devices.js` → `traffic.js` → `rules.js` → `viewer.js` → `share.js` → `app.js` |

## JS modules — key functions

| File | Key functions |
|---|---|
| `utils.js` | `$`, `STATUS`, `showError`, `relTime`, `clockTime`, `esc`, `formatBody`, `shortId`, `methodCls`, `newUuid` |
| `api.js` | `getToken` / `setToken`, `apiFetch` (adds Bearer header), `doLogin`, `doRegister`, `doLogout` |
| `json-tree.js` | `JSON_TREE_MAX_NODES`, `jsonTreeHtml`, `jvCount`, `jvNode`, `bodyHtml`, `bindJsonTree` |
| `devices.js` | `loadDevices`, `render`, `connect`, `disconnect`, `backToList`, `deleteDevice`, `renameDevice`, `openAddDeviceModal`, `closeAddDeviceModal`, `submitAddDevice` |
| `traffic.js` | `openDetail`, `enterDetail`, `loadDetail`, `bindSession`, `stopTrafficPoll`, `showDetailPane`, `pollTraffic`, `mergeLog`, `renderTraffic`, `deleteTrafficEntry`, `clearTrafficLog`, `renderTrafficDetail` |
| `rules.js` | `loadRules`, `renderRules`, `renderRuleDetail`, `openEditRuleForm` (CodeMirror), `saveRuleEdit`, `deleteRule`, `toggleRule`, `mockThisRequest` |
| `viewer.js` | `registerViewer`, `renewViewer`, `stopViewer`, `releaseAllViewers` (+ `pagehide` listener) |
| `share.js` | `renderShareView` (public), `shareRequest` |
| `app.js` | Constants (`API`, `POLL_MS`, `TRAFFIC_POLL_MS`, `TRAFFIC_PAGE`, `RENEW_MS`, `LOG_CAP`), state (`viewers`, `pollTimer`, `detail`, `ruleStore`, `pageLog`, `trafficFilter`, …), hash routing, `showAuth`/`showAuthError`/`hideAuthError`, `enterApp`, `boot`, wiring IIFEs (traffic filter input, add-device modal) |

## `style.css`

Layout for the auth card, device list cards, two-column detail grid, badges (idle/capturing/offline), conflict banner, modal, CodeMirror editor, delete buttons. Device delete button color/position was hardened with inline styles + `!important` (M8.5 history).

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
