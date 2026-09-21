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
		ServerConfig: a.captureManager.ServerConfig(),
	})
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
		ServerConfig: a.captureManager.ServerConfig(),
		Session:      session,
		RulesVersion: rulesVersion,
	})
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
	session, err := a.captureManager.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// handleEndSession handles DELETE /api/v1/sessions/{id} (Web「断开」).
func (a *API) handleEndSession(w http.ResponseWriter, r *http.Request) {
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

	lease, err := a.captureManager.RegisterViewer(r.Context(), r.PathValue("id"), req.ViewerID, req.Label)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleReleaseViewer handles DELETE /api/v1/sessions/{id}/viewers/{viewerId}.
func (a *API) handleReleaseViewer(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, DeviceListResponse{Devices: devices, Total: len(devices)})
}

// handleGetDevice handles GET /api/v1/devices/{app}/{did}.
func (a *API) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	view, err := a.captureManager.GetDevice(r.Context(), r.PathValue("app"), r.PathValue("did"))
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
