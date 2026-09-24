// MockNetPack capture API: shared contract schemas, error mapping and helpers.
// Handlers live in device_handlers.go / traffic_handlers.go (pure move from
// this file); routes live under /api/v1 (the OpenAPI contract base path),
// separate from mockd's native /sessions proxy-recording routes.
package admin

import (
	"errors"
	"net/http"
	"strconv"
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

// UpdateDeviceNameRequest is the M7.2.2 rename payload.
type UpdateDeviceNameRequest struct {
	Name string `json:"name"`
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
