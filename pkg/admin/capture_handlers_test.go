package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/getmockd/mockd/pkg/store/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCaptureTestAPI builds an admin API (no API key, temp data dir) served via
// httptest. The health-check goroutine is intentionally not started (Start is
// never called), so these tests exercise the request paths deterministically.
func newCaptureTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithAPIKeyDisabled(),
		WithCaptureConfig(store.CaptureConfig{
			HeartbeatInterval: 20 * time.Second,
			HeartbeatTimeout:  60 * time.Second,
			ViewerTTL:         120 * time.Second,
		}),
	)
	srv := httptest.NewServer(api.httpServer.Handler)
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url string, body any, out any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, url, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp
}

func TestCaptureAPI_DeviceHeartbeatSessionLifecycle(t *testing.T) {
	srv := newCaptureTestAPI(t)

	// Register.
	var reg RegisterDeviceResponse
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.app", Did: "dev-1", OSVersion: "17.5", SDKVersion: "0.1.0"}, &reg)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, reg.Device)
	assert.Equal(t, "com.example.app", reg.Device.App)
	assert.Equal(t, "dev-1", reg.Device.Did)
	assert.Equal(t, capture.DeviceStatusIdle, reg.Device.Status)
	assert.Equal(t, 20, reg.ServerConfig.HeartbeatIntervalSeconds)
	assert.Equal(t, 60, reg.ServerConfig.HeartbeatTimeoutSeconds)

	// Heartbeat with no session.
	var hb HeartbeatResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, hb.OK)
	assert.Nil(t, hb.Session, "heartbeat before activation must carry no session")

	// Activate from Web.
	var session capture.CaptureSession
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.app", Did: "dev-1"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, capture.SessionStatusCapturing, session.Status)
	sessionID := session.ID

	// Heartbeat now carries the session (SDK starts capture).
	hb = HeartbeatResponse{}
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, hb.Session)
	assert.Equal(t, sessionID, hb.Session.ID)

	// Device list shows capturing.
	var devices DeviceListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices", nil, &devices)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, devices.Devices, 1)
	assert.Equal(t, capture.DeviceStatusCapturing, devices.Devices[0].Status)
	require.NotNil(t, devices.Devices[0].CurrentSession)
	assert.Equal(t, sessionID, devices.Devices[0].CurrentSession.ID)

	// GET single device.
	var dv capture.DeviceView
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices/com.example.app/dev-1", nil, &dv)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, capture.DeviceStatusCapturing, dv.Status)

	// Web disconnects.
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/"+sessionID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// M9 (会话结束即删): the session record is gone after disconnect.
	var errResp ErrorResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID, nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)

	// Heartbeat now carries no session (SDK stops capture).
	hb = HeartbeatResponse{}
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(t, hb.Session, "heartbeat after session end must carry no session")

	// Device back to idle.
	devices = DeviceListResponse{}
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices", nil, &devices)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, capture.DeviceStatusIdle, devices.Devices[0].Status)
	assert.Nil(t, devices.Devices[0].CurrentSession)
}

func TestCaptureAPI_MultiDeviceIsolation(t *testing.T) {
	srv := newCaptureTestAPI(t)

	for _, d := range []*capture.Device{
		{App: "app-a", Did: "dev-1"},
		{App: "app-a", Did: "dev-2"},
		{App: "app-b", Did: "dev-1"},
	} {
		resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
			RegisterDeviceRequest{App: d.App, Did: d.Did}, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, "register %s/%s", d.App, d.Did)
	}

	// Activate only app-a/dev-1.
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "app-a", Did: "dev-1"}, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var devices DeviceListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices", nil, &devices)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, devices.Devices, 3)

	byKey := map[string]*capture.DeviceView{}
	for _, v := range devices.Devices {
		byKey[v.App+"/"+v.Did] = v
	}
	assert.Equal(t, capture.DeviceStatusCapturing, byKey["app-a/dev-1"].Status)
	assert.Equal(t, capture.DeviceStatusIdle, byKey["app-a/dev-2"].Status)
	assert.Equal(t, capture.DeviceStatusIdle, byKey["app-b/dev-1"].Status)

	// Sessions are isolated per device: app-b/dev-1 has none.
	var list SessionListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions?app=app-b&did=dev-1", nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, list.Sessions)

	list = SessionListResponse{}
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions?app=app-a&did=dev-1", nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, list.Sessions, 1)
	assert.Equal(t, capture.SessionStatusCapturing, list.Sessions[0].Status)

	// Heartbeat for app-b/dev-1 must not see app-a/dev-1's session.
	var hb HeartbeatResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/app-b/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(t, hb.Session)
}

func TestCaptureAPI_ViewerLifecycle(t *testing.T) {
	srv := newCaptureTestAPI(t)

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "app", Did: "dev-1"}, nil)

	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "app", Did: "dev-1"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	sessionID := session.ID

	// Register two viewers.
	var lease capture.ViewerLease
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+sessionID+"/viewers",
		RegisterViewerRequest{ViewerID: "v1", Label: "page-a"}, &lease)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "v1", lease.ViewerID)
	assert.Equal(t, 120, lease.TTLSeconds)

	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+sessionID+"/viewers",
		RegisterViewerRequest{ViewerID: "v2"}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got capture.CaptureSession
	doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID, nil, &got)
	assert.Equal(t, 2, got.ViewerCount)

	// Release one viewer: still capturing.
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/"+sessionID+"/viewers/v1", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID, nil, &got)
	assert.Equal(t, 1, got.ViewerCount)
	assert.Equal(t, capture.SessionStatusCapturing, got.Status)

	// Release the last viewer: session ends and its record is deleted (M9).
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/"+sessionID+"/viewers/v2", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	var errResp ErrorResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID, nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)

	// Registering a viewer on a deleted session is rejected.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+sessionID+"/viewers",
		RegisterViewerRequest{ViewerID: "v3"}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)
}

func TestCaptureAPI_ErrorPaths(t *testing.T) {
	srv := newCaptureTestAPI(t)

	// Heartbeat for unregistered device -> 404.
	var errResp ErrorResponse
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/app/nope/heartbeat", nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "device_not_registered", errResp.Error)

	// Activate unregistered device -> 404.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "app", Did: "nope"}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "device_not_registered", errResp.Error)

	// Register with missing app -> 400.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{Did: "dev-1"}, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "missing_app", errResp.Error)

	// Activate an offline device -> 409 device_offline. Construct the stale
	// device in a store, flush to disk, then open a fresh API over the same
	// directory so the handler sees it.
	dir := t.TempDir()
	fs := file.New(store.Config{DataDir: dir})
	require.NoError(t, fs.Open(t.Context()))
	require.NoError(t, fs.Devices().Create(t.Context(), &capture.Device{
		App: "app", Did: "stale",
		RegisteredAt: time.Now().Add(-time.Hour),
		LastSeenAt:   time.Now().Add(-time.Hour),
	}))
	require.NoError(t, fs.Close())

	api := NewAPI(0,
		WithDataDir(dir),
		WithAPIKeyDisabled(),
		WithCaptureConfig(store.DefaultCaptureConfig()),
	)
	srv2 := httptest.NewServer(api.httpServer.Handler)
	defer srv2.Close()

	errResp = ErrorResponse{}
	resp = doJSON(t, http.MethodPost, srv2.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "app", Did: "stale"}, &errResp)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "device_offline", errResp.Error)

	// End an unknown session -> 404.
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/nonexistent", nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCaptureAPI_TrafficLifecycle(t *testing.T) {
	srv := newCaptureTestAPI(t)

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.app", Did: "dev-1"}, nil)
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.app", Did: "dev-1"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	sessionID := session.ID

	// Upload: 2 valid + 1 invalid (empty method) -> 202, count=2.
	now := time.Now()
	entries := []*capture.TrafficEntry{
		{
			Timestamp: now, Method: "POST", URL: "http://example.com/api/feed/list?page=1",
			Path: "/api/feed/list", Query: "page=1",
			RequestHeaders:  map[string][]string{"Content-Type": {"application/json"}},
			RequestBody:     `{"page":1}`,
			StatusCode:      200,
			ResponseHeaders: map[string][]string{"Server": {"mockd"}},
			ResponseBody:    `{"items":[]}`,
			DurationMs:      42,
		},
		{Timestamp: now.Add(time.Millisecond), Method: "GET", URL: "http://example.com/health", DurationMs: 3},
		{Timestamp: now.Add(2 * time.Millisecond), Method: "", URL: "http://x/bad", DurationMs: 1}, // invalid
	}
	var up TrafficUploadResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "com.example.app", Did: "dev-1", SessionID: sessionID, Entries: entries}, &up)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.True(t, up.Accepted)
	assert.Equal(t, 2, up.Count)

	// Session RequestCount reflects accepted entries.
	var gotSession capture.CaptureSession
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID, nil, &gotSession)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, gotSession.RequestCount)

	// Request stream with full detail fields.
	var list TrafficListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic", nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, list.Total)
	require.Len(t, list.Entries, 2)
	first := list.Entries[0]
	assert.Equal(t, "POST", first.Method)
	assert.Equal(t, "http://example.com/api/feed/list?page=1", first.URL)
	assert.Equal(t, "/api/feed/list", first.Path)
	assert.Equal(t, "page=1", first.Query)
	assert.Equal(t, `{"page":1}`, first.RequestBody)
	assert.Equal(t, 200, first.StatusCode)
	assert.Equal(t, `{"items":[]}`, first.ResponseBody)
	assert.Equal(t, 42, first.DurationMs)
	assert.NotEmpty(t, first.ID)
	assert.Equal(t, sessionID, first.SessionID)

	// Single request detail by server-generated ID.
	var detail capture.TrafficEntry
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/traffic/"+first.ID, nil, &detail)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, first.URL, detail.URL)

	// Paging: limit=1&offset=1 -> second entry only.
	var page TrafficListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?limit=1&offset=1", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "GET", page.Entries[0].Method)

	// End session (M9: 结束即删) -> session and its traffic are gone: 404.
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/"+sessionID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	var errResp ErrorResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic", nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/traffic/"+first.ID, nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errResp.Error)
}

func TestCaptureAPI_TrafficErrors(t *testing.T) {
	srv := newCaptureTestAPI(t)

	validEntry := func() *capture.TrafficEntry {
		return &capture.TrafficEntry{Timestamp: time.Now(), Method: "GET", URL: "http://x", DurationMs: 1}
	}

	// Unknown session upload -> 404 session_not_found.
	var errResp ErrorResponse
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "d1", SessionID: "nope", Entries: []*capture.TrafficEntry{validEntry()}}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "app", Did: "d1"}, nil)
	var session capture.CaptureSession
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "app", Did: "d1"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	sessionID := session.ID

	// Empty entries -> 400.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "d1", SessionID: sessionID, Entries: []*capture.TrafficEntry{}}, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// All entries invalid -> 400 invalid_field.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "d1", SessionID: sessionID,
			Entries: []*capture.TrafficEntry{{Method: "", URL: "http://x", Timestamp: time.Now(), DurationMs: 1}}}, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_field", errResp.Error)

	// More than 500 entries -> 400.
	big := make([]*capture.TrafficEntry, 501)
	for i := range big {
		big[i] = validEntry()
	}
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "d1", SessionID: sessionID, Entries: big}, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Cross-device upload (isolation) -> 404 session_not_found.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "other", SessionID: sessionID, Entries: []*capture.TrafficEntry{validEntry()}}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)

	// Invalid limit -> 400; limit beyond 500 -> 400.
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?limit=abc", nil, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?limit=501", nil, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Unknown session list -> 404 session_not_found.
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/nope/traffic", nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)

	// Ended session (M9: 结束即删) upload -> 404 session_not_found.
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/sessions/"+sessionID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "app", Did: "d1", SessionID: sessionID, Entries: []*capture.TrafficEntry{validEntry()}}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "session_not_found", errResp.Error)
}
