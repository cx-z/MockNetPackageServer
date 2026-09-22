// MockNetPack capture API handlers: device registration, heartbeat, capture
// session lifecycle and viewer leases. All routes live under /api/v1 (the
// OpenAPI contract base path), which also keeps them separate from mockd's
// native /sessions proxy-recording routes.

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// captureAPIPrefix is the base path for all MockNetPack endpoints (contract).
const captureAPIPrefix = "/api/v1"

// ============================================================================
// Request / response types (contract schemas)
// ============================================================================

// RegisterDeviceRequest is the SDK registration payload (contract schema).
type RegisterDeviceRequest struct {
	App        string           `json:"app"`
	Did        string           `json:"did"`
	Platform   capture.Platform `json:"platform,omitempty"`
	OSVersion  string           `json:"osVersion,omitempty"`
	SDKVersion string           `json:"sdkVersion,omitempty"`
	AppVersion string           `json:"appVersion,omitempty"`
}

// CreateDeviceRequest is the Web manual-registration payload (M7.2.1): pick an
// app from the fixed catalog, type the SDK did, and give the device a name.
type CreateDeviceRequest struct {
	App  string `json:"app"`
	Did  string `json:"did"`
	Name string `json:"name"`
}

// allowedApps is the fixed app catalog (M7 拍板 #5): only one app for now;
// admin-only app management is a recorded backlog item.
var allowedApps = map[string]bool{"com.example.integrating": true}

// handleCreateDevice handles POST /api/v1/devices — Web manual device
// registration. The logged-in user becomes the owner; an existing (App, Did)
// conflicts. SDK auto-registration is removed in M7.2.3, making this the only
// creation path afterwards.
func (a *API) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req CreateDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.App = strings.TrimSpace(req.App)
	req.Did = strings.TrimSpace(req.Did)
	req.Name = strings.TrimSpace(req.Name)
	if req.App == "" || req.Did == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "app, did and name are required")
		return
	}
	if !allowedApps[req.App] {
		writeError(w, http.StatusBadRequest, "invalid_app", "app is not in the allowed catalog")
		return
	}
	if len(req.Did) > 128 || len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_field", "did must be <=128 chars, name <=64 chars")
		return
	}
	owner := ""
	if u := currentUser(r); u != nil {
		owner = u.Username
	}
	d := &capture.Device{
		App:      req.App,
		Did:      req.Did,
		Name:     req.Name,
		Owner:    owner,
		Platform: capture.PlatformIOS,
	}
	if _, err := a.captureManager.CreateManualDevice(r.Context(), d); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "device_taken", "device already registered")
			return
		}
		writeCaptureError(w, err)
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// RegisterDeviceResponse is returned on successful registration.
type RegisterDeviceResponse struct {
	Device       *capture.DeviceView  `json:"device"`
	ServerConfig capture.ServerConfig `json:"serverConfig"`
}

// CaptureHeartbeatRequest is the SDK heartbeat payload (optional).
type CaptureHeartbeatRequest struct {
	SDKVersion string `json:"sdkVersion,omitempty"`
}

// HeartbeatResponse carries the session state back to the SDK: the SDK starts
// capture when session is non-null and stops when it is null.
type HeartbeatResponse struct {
	OK           bool                    `json:"ok"`
	ServerTime   time.Time               `json:"serverTime"`
	ServerConfig capture.ServerConfig    `json:"serverConfig"`
	Session      *capture.CaptureSession `json:"session"`
	RulesVersion int                     `json:"rulesVersion"`
}

// ActivateSessionRequest activates a capture session from the Web UI.
type ActivateSessionRequest struct {
	App string `json:"app"`
	Did string `json:"did"`
}

// DeviceListResponse is the Web device list payload.
type DeviceListResponse struct {
	Devices []*capture.DeviceView `json:"devices"`
	Total   int                   `json:"total"`
}

// SessionListResponse is the Web capture session list payload.
type SessionListResponse struct {
	Sessions []*capture.CaptureSession `json:"sessions"`
	Total    int                       `json:"total"`
}

// RegisterViewerRequest registers/renews a page-level viewer lease.
type RegisterViewerRequest struct {
	ViewerID string `json:"viewerId"`
	Label    string `json:"label,omitempty"`
}

// TrafficUploadRequest is the SDK traffic batch payload (contract schema:
// app, did, sessionId, entries, max 500 items).
type TrafficUploadRequest struct {
	App       string                  `json:"app"`
	Did       string                  `json:"did"`
	SessionID string                  `json:"sessionId"`
	Entries   []*capture.TrafficEntry `json:"entries"`
}

// TrafficUploadResponse is returned on successful ingestion (202; count is the
// number of entries actually stored — partial acceptance is allowed).
type TrafficUploadResponse struct {
	Accepted bool `json:"accepted"`
	Count    int  `json:"count"`
}

// TrafficListResponse is the Web request-stream payload.
type TrafficListResponse struct {
	Entries []*capture.TrafficEntry `json:"entries"`
	Total   int                     `json:"total"`
}

// ============================================================================
// Handlers
// ============================================================================

// handleRegisterDevice handles POST /api/v1/devices/register.
func (a *API) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.App == "" {
		writeError(w, http.StatusBadRequest, "missing_app", "app is required")
		return
	}
	if req.Did == "" {
		writeError(w, http.StatusBadRequest, "missing_did", "did is required")
		return
	}
	if len(req.App) > 128 || len(req.Did) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_field", "app and did must be at most 128 characters")
		return
	}

	// M7.2.3: SDK auto-registration is retired. A device must already exist
	// (created manually in the Web UI). An unknown did gets a technical 404 —
	// no user-facing "please register in Web" copy here; that guidance belongs
	// to the Web UI, not the debug SDK channel.
	if _, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did); err != nil {
		writeCaptureError(w, err)
		return
	}
	d := &capture.Device{
		App:        req.App,
		Did:        req.Did,
		Platform:   req.Platform,
		OSVersion:  req.OSVersion,
		SDKVersion: req.SDKVersion,
		AppVersion: req.AppVersion,
	}
	if _, err := a.captureManager.RegisterDevice(r.Context(), d); err != nil {
		writeCaptureError(w, err)
		return
	}

	view, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RegisterDeviceResponse{
		Device:       view,
		ServerConfig: idleHeartbeatConfig(a.captureManager.ServerConfig()),
	})
}

// idleHeartbeatConfig 返回 idle（未抓包）状态的心跳间隔（M8.2：5s，快速感知会话激活）。
func idleHeartbeatConfig(cfg capture.ServerConfig) capture.ServerConfig {
	cfg.HeartbeatIntervalSeconds = 5
	return cfg
}

// capturingHeartbeatConfig 返回 capturing（抓包中）状态的心跳间隔（M8.3：3s，快速感知规则变更）。
func capturingHeartbeatConfig(cfg capture.ServerConfig) capture.ServerConfig {
	cfg.HeartbeatIntervalSeconds = 3
	return cfg
}

// handleDeviceHeartbeat handles POST /api/v1/devices/{app}/{did}/heartbeat.
func (a *API) handleDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")

	var req CaptureHeartbeatRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}

	_, session, err := a.captureManager.Heartbeat(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	rulesVersion, err := a.captureManager.RuleVersion(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HeartbeatResponse{
		OK:           true,
		ServerTime:   time.Now(),
		// M8.2/M8.3：心跳间隔按会话状态动态下发——capturing 3s（快速感知规则变更）、
		// idle 5s（快速感知会话激活）；SDK 按响应间隔调度下一次心跳。
		ServerConfig: heartbeatConfigForSession(a.captureManager.ServerConfig(), session),
		Session:      session,
		RulesVersion: rulesVersion,
	})
}

// heartbeatConfigForSession 根据会话是否激活返回对应心跳间隔配置。
func heartbeatConfigForSession(base capture.ServerConfig, session *capture.CaptureSession) capture.ServerConfig {
	if session != nil {
		return capturingHeartbeatConfig(base)
	}
	return idleHeartbeatConfig(base)
}

// handleActivateSession handles POST /api/v1/sessions (Web「连接」).
func (a *API) handleActivateSession(w http.ResponseWriter, r *http.Request) {
	var req ActivateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.App == "" || req.Did == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app and did are required")
		return
	}
	if !a.authorizeDeviceAccess(w, r, req.App, req.Did) {
		return
	}

	session, created, err := a.captureManager.ActivateSession(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, session)
}

// handleListSessions handles GET /api/v1/sessions?app=&did=.
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	app := q.Get("app")
	did := q.Get("did")
	if app == "" || did == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app and did query parameters are required (device isolation)")
		return
	}

	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	filter := &store.SessionFilter{App: &app, Did: &did}
	sessions, err := a.captureManager.ListSessions(r.Context(), filter)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: sessions, Total: len(sessions)})
}

// handleGetSession handles GET /api/v1/sessions/{id}.
func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sid, ok := a.authorizeSessionAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sid)
}

// handleEndSession handles DELETE /api/v1/sessions/{id} (Web「断开」).
func (a *API) handleEndSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.EndSession(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRegisterViewer handles POST /api/v1/sessions/{id}/viewers.
func (a *API) handleRegisterViewer(w http.ResponseWriter, r *http.Request) {
	var req RegisterViewerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.ViewerID == "" {
		writeError(w, http.StatusBadRequest, "missing_viewer_id", "viewerId is required")
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}

	lease, err := a.captureManager.RegisterViewer(r.Context(), r.PathValue("id"), req.ViewerID, req.Label)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleReleaseViewer handles DELETE /api/v1/sessions/{id}/viewers/{viewerId}.
func (a *API) handleReleaseViewer(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.ReleaseViewer(r.Context(), r.PathValue("id"), r.PathValue("viewerId")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListDevices handles GET /api/v1/devices (Web device list).
func (a *API) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := a.captureManager.ListDevices(r.Context(), nil)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	// M7.2.2 ownership filtering: admin sees all; a dev sees only its own devices.
	if u := currentUser(r); u != nil && !isAdmin(u) {
		filtered := make([]*capture.DeviceView, 0, len(devices))
		for _, d := range devices {
			if d.Owner == u.Username {
				filtered = append(filtered, d)
			}
		}
		devices = filtered
	}
	writeJSON(w, http.StatusOK, DeviceListResponse{Devices: devices, Total: len(devices)})
}

// handleGetDevice handles GET /api/v1/devices/{app}/{did}.
func (a *API) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	app, did := r.PathValue("app"), r.PathValue("did")
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// UpdateDeviceNameRequest is the M7.2.2 rename payload.
type UpdateDeviceNameRequest struct {
	Name string `json:"name"`
}

// handleUpdateDeviceName handles PUT /api/v1/devices/{app}/{did} — rename a
// device. Ownership is enforced (owner or admin only; others see 404).
func (a *API) handleUpdateDeviceName(w http.ResponseWriter, r *http.Request) {
	app, did := r.PathValue("app"), r.PathValue("did")
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	var req UpdateDeviceNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "name is required")
		return
	}
	if len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_field", "name must be <=64 characters")
		return
	}
	if _, err := a.captureManager.UpdateDeviceName(r.Context(), app, did, req.Name); err != nil {
		writeCaptureError(w, err)
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUploadTraffic handles POST /api/v1/traffic (SDK batch upload, 全量抓包).
// Contract semantics: 202 on acceptance (partial acceptance allowed — count is
// the number of entries stored); 400 when the batch is empty or every entry is
// invalid; 404 session_not_found for an unknown session; 409 session_ended for
// an ended session.
func (a *API) handleUploadTraffic(w http.ResponseWriter, r *http.Request) {
	var req TrafficUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.App == "" || req.Did == "" || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app, did and sessionId are required")
		return
	}
	if len(req.Entries) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "entries must not be empty")
		return
	}
	if len(req.Entries) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "entries must be at most 500 items")
		return
	}

	count, err := a.captureManager.UploadTraffic(r.Context(), req.App, req.Did, req.SessionID, req.Entries)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if count == 0 {
		// Every entry was invalid (empty method/url or zero timestamp).
		writeError(w, http.StatusBadRequest, "invalid_field", "all traffic entries are invalid")
		return
	}
	writeJSON(w, http.StatusAccepted, TrafficUploadResponse{Accepted: true, Count: count})
}

// handleGetTraffic handles GET /api/v1/traffic/{id} (single request detail).
func (a *API) handleGetTraffic(w http.ResponseWriter, r *http.Request) {
	entry, err := a.captureManager.GetTraffic(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, entry.SessionID); !ok {
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleListSessionTraffic handles GET /api/v1/sessions/{id}/traffic (Web
// request stream — the 2s-polling push channel; no extra push endpoint is
// needed, decision D-M2-1). limit defaults to 100 and must be 1..500 (0 means
// default); offset defaults to 0.
func (a *API) handleListSessionTraffic(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be an integer")
		return
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be between 1 and 500")
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be an integer")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be non-negative")
		return
	}

	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	entries, total, err := a.captureManager.ListSessionTraffic(r.Context(), r.PathValue("id"), limit, offset)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TrafficListResponse{Entries: entries, Total: total})
}

// handleDeleteTraffic handles DELETE /api/v1/traffic/{id} (Web per-row
// "删除"; M9.5). 204 on success; 404 not_found for an unknown entry — the Web
// treats delete as best-effort (the entry may belong to an already-deleted
// session).
func (a *API) handleDeleteTraffic(w http.ResponseWriter, r *http.Request) {
	entry, err := a.captureManager.GetTraffic(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, entry.SessionID); !ok {
		return
	}
	if err := a.captureManager.DeleteTraffic(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClearSessionTraffic handles DELETE /api/v1/sessions/{id}/traffic
// (Web "清空日志"; M9.5). 204 on success; 404 session_not_found for an unknown
// session.
func (a *API) handleClearSessionTraffic(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.ClearSessionTraffic(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// queryInt parses an integer query parameter, returning def when the parameter
// is absent or empty.
func queryInt(r *http.Request, key string, def int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// writeCaptureError maps capture/store errors to contract error responses.
func writeCaptureError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDeviceNotRegistered):
		writeError(w, http.StatusNotFound, "device_not_registered", "Device is not registered")
	case errors.Is(err, store.ErrDeviceOffline):
		writeError(w, http.StatusConflict, "device_offline", "Device is offline (heartbeat timeout)")
	case errors.Is(err, store.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "session_not_found", "Capture session not found")
	case errors.Is(err, store.ErrSessionEnded):
		writeError(w, http.StatusConflict, "session_ended", "Capture session has ended")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
	case errors.Is(err, store.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, "rule_not_found", "Mock rule not found")
	case errors.Is(err, store.ErrRuleConflict):
		writeError(w, http.StatusConflict, "rule_conflict", store.MockRuleConflictMessage)
	case errors.Is(err, store.ErrNoteRequired):
		writeError(w, http.StatusBadRequest, "missing_field", "note is required when editing the canned response")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "already_exists", "Resource already exists")
	case errors.Is(err, store.ErrReadOnly):
		writeError(w, http.StatusConflict, "read_only", "Store is read-only")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
	}
}

// ============================================================================
// M8.5: Request share links
// ============================================================================

// shareRequest is the POST /shares body: which traffic entry to snapshot.
type shareRequest struct {
	TrafficID string `json:"trafficId"`
}

// shareResponse is the POST /shares reply: the opaque shareId and public URL.
type shareResponse struct {
	ShareID   string `json:"shareId"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// handleCreateShare handles POST /api/v1/shares (authenticated). It snapshots
// a single traffic entry into an independent share store so the share survives
// session/traffic deletion.
func (a *API) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req shareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "request body must be JSON")
		return
	}
	if req.TrafficID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "trafficId is required")
		return
	}
	// Verify the caller owns the session that contains this traffic entry.
	entry, err := a.captureManager.GetTraffic(r.Context(), req.TrafficID)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, entry.SessionID); !ok {
		return
	}
	snap, err := a.captureManager.CreateShare(r.Context(), req.TrafficID)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, shareResponse{
		ShareID:   snap.ShareID,
		URL:       "/mocknetpack/#/share/" + snap.ShareID,
		ExpiresAt: snap.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleGetShare handles GET /api/v1/shares/{id} (public, no auth). Returns the
// read-only snapshot. Expired or unknown shares return 404.
func (a *API) handleGetShare(w http.ResponseWriter, r *http.Request) {
	snap, err := a.captureManager.GetShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
