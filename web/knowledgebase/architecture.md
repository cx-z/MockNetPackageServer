# MockNetPack Web — Architecture (L2)

> KB layer L2. Read [`overview.md`](overview.md) first. Next: [`code-map.md`](code-map.md).

## App shell & boot

```
index.html loads → app.js boot()
  → wire auth forms + logout + add-device modal
  → hash route?
      #/share/{id}  → shareViewActive = true; renderShareView(id); return   (no auth)
      else          → token in localStorage?
          yes → GET /auth/me → ok? enterApp() : clear token → showAuth()
          no  → showAuth()   // login/register gate
enterApp()
  → hide auth view; show appWrap; set user info + role label
  → loadDevices(); pollTimer = setInterval(loadDevices, 5000)
  → if hash is #/device/{app}/{did} → enterDetail(...) (refresh deep-link)
```

## Views & state

| State | Where | Purpose |
|---|---|---|
| `token` | `localStorage["mocknetpack_token"]` | Bearer auth on every `apiFetch` |
| `authUser` | module var | `{username, role}` from login/me; drives role label |
| `detail` | module var | `{app, did, sessionId, viewerId, …}` of the open device |
| `pageLog` | module var | page-level traffic buffer (cap 300); survives disconnect, merges on reconnect |
| `viewers` | `Map` | viewerId → interval, for lease renew/cleanup |
| `trafficFilter` | module var | ephemeral string filter (page-level display rule) |
| `pollTimer` | module var | device-list 5s poll |

## Data flows

### 1. Device list → connect → live traffic

```
loadDevices() → GET /api/v1/devices → render(devices)          // every 5s
  card shows: did, name, status badge (idle/capturing/offline derived server-side),
              last seen, session button, rename, delete

connect(d)   → POST /api/v1/sessions {app,did}                 // activate capture
             → registerViewer(session) → POST /sessions/{id}/viewers {viewerId}
             → bindSession(session) → detail.sessionId set
             → enterDetail(app, did) → loadDetail() → startTrafficPoll()

disconnect(d)→ POST... no — DELETE /api/v1/sessions/{id}       // disconnect
             → stopTrafficPoll(); releaseAllViewers(); clear pageLog binding
             (list buttons connect/disconnect removed in M8.x; control lives on detail)

loadDetail() → GET /api/v1/devices/{app}/{did} → render device card
             → loadRules() (right after) 
pollTraffic()→ GET /api/v1/sessions/{id}/traffic?limit=100 every 2000ms
             → mergeLog(base, fresh) (reconnect merges into one timeline)
             → renderTraffic(pageLog) with trafficFilter applied
             → row click → renderTrafficDetail(e)
```

### 2. Mock rules

```
loadRules() → GET /api/v1/devices/{app}/{did}/mock-rules       // full list (Bearer)
renderRules(data) → rules list; effective badge; conflict banner
   (server returns effective=false for multi-enabled interfaces → banner/popup)

mockThisRequest(e) → POST /api/v1/devices/{app}/{did}/mock-rules
     {method, path, response: from captured entry, source: snapshot, enabled: true?}
     → creates rule from real response b1 ("Mock 此请求")

openEditRuleForm(r) → inline right-column editor (CodeMirror JSON folding)
saveRuleEdit(rule) → PUT /api/v1/devices/{app}/{did}/mock-rules/{ruleId}
     {response, note, enabled?}  // match key & source immutable; blank note on real edit ⇒ 400
     → client-side JSON validation before save; conflict 409 shown inline

toggleRule(rule, enabled) → PUT (echo response, note kept, enabled flipped)
deleteRule(rule) → DELETE /api/v1/devices/{app}/{did}/mock-rules/{ruleId}
```

### 3. Viewer leases (session keep-alive across tabs)

```
registerViewer(session)  → POST /sessions/{id}/viewers {viewerId: newUuid()}
renewViewer(sessionId, viewerId) → same POST every RENEW_MS (60s, TTL 120s)
stopViewer / releaseAllViewers → DELETE /sessions/{id}/viewers/{viewerId}
  (last viewer release ends the session server-side)
```

### 4. Page-level log semantics (M6.4/M6.5/M9)

```
- disconnect: traffic poll stops, but pageLog RETAINED on the page
- reconnect: mergeLog merges old + new into one timeline (mixed order by time)
- back/refresh/close: pageLog cleared; ended sessions are gone server-side (delete-on-end)
- delete row / clear: DELETE /traffic/{id} / DELETE /sessions/{id}/traffic
  (log-only; never touches mock rules)
- display rule: trafficFilter input filters pageLog in-memory only (ephemeral, refresh resets)
```

### 5. Share links (M8.5)

```
shareRequest(e) → POST /api/v1/shares {trafficId} → {shareId, url, expiresAt}
   copy url /mocknetpack/#/share/{shareId}
boot() with #/share/{id} → renderShareView(id) → GET /api/v1/shares/{id} (public)
   → read-only snapshot rendering (no auth, no app chrome)
```

### 6. Auth

```
doLogin    → POST /auth/login {username,password} → setToken(token); authUser; enterApp()
doRegister → POST /auth/register → auto-login → enterApp()
doLogout   → POST /auth/logout (server-side revoke) → clear token/state/polls →
             back to list view + showAuth()
```

## UX / error patterns

| Pattern | Behavior |
|---|---|
| `apiFetch` | Adds `Authorization: Bearer` when token present; normalizes error responses |
| Rule conflict | Server 409 / conflict list → banner + popup text "不允许同一个接口同时开启多个 Mock 规则" |
| JSON display | `jsonTreeHtml` tree with fold/unfold (cap 5000 nodes → fallback `<pre>`); body tab fallback `[binary N bytes]` |
| Rule editor | CodeMirror with fold gutter + bracket matching; save-time JSON validation; inline in right column |
| Delete/clear | Best-effort idempotent (404 tolerated — entry may belong to a deleted session) |
| Disconnect UI | Connect/disconnect only on detail device card (list-level buttons removed) |
| Auth errors | Inline `authError`; 401 on /auth/me → drop token → login gate |

## API call map (function → endpoint)

| Function | Endpoint |
|---|---|
| `loadDevices` | `GET /devices` |
| `connect` | `POST /sessions` |
| `disconnect` | `DELETE /sessions/{id}` |
| `loadDetail` | `GET /devices/{app}/{did}` |
| `pollTraffic` | `GET /sessions/{id}/traffic?limit=100` |
| `deleteTrafficEntry` | `DELETE /traffic/{id}` |
| `clearTrafficLog` | `DELETE /sessions/{id}/traffic` |
| `loadRules` | `GET /devices/{app}/{did}/mock-rules` |
| `mockThisRequest` | `POST /devices/{app}/{did}/mock-rules` |
| `saveRuleEdit` / `toggleRule` | `PUT /devices/{app}/{did}/mock-rules/{ruleId}` |
| `deleteRule` | `DELETE /devices/{app}/{did}/mock-rules/{ruleId}` |
| `registerViewer` / `renewViewer` | `POST /sessions/{id}/viewers` |
| `releaseAllViewers` | `DELETE /sessions/{id}/viewers/{viewerId}` |
| `submitAddDevice` | `POST /devices` |
| `renameDevice` | `PUT /devices/{app}/{did}` |
| `deleteDevice` | `DELETE /devices/{app}/{did}` |
| `doLogin` / `doRegister` | `POST /auth/login` / `POST /auth/register` |
| `doLogout` | `POST /auth/logout` |
| `boot` (restore) | `GET /auth/me` |
| `shareRequest` | `POST /shares` |
| `renderShareView` | `GET /shares/{id}` (public) |
