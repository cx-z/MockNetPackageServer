package admin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDKRegisterUnknownDevice404: M7.2.3 retires SDK auto-registration. An
// unknown did must get a technical 404 (no user-facing "register in Web" copy
// on the SDK channel); a known device refreshes metadata with 200.
func TestSDKRegisterUnknownDevice404(t *testing.T) {
	srv := newCaptureTestAPI(t)

	// Unknown did -> 404 device_not_registered (technical code only).
	var errResp ErrorResponse
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.integrating", Did: "never-registered"}, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "device_not_registered", errResp.Error)

	// Seed a device, then register again -> 200 (metadata refresh, not create).
	mustSeedDevice(t, srv, "com.example.integrating", "known-dev")
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.integrating", Did: "known-dev", SDKVersion: "0.3.0"}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Heartbeat for an unknown did -> 404 (already enforced pre-M7.2.3).
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/ghost/heartbeat", nil, &errResp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "device_not_registered", errResp.Error)
}

// TestSDKRegisterAppName: v0.9.0 — the SDK reports the app display name
// (CFBundleDisplayName, e.g. "IntegratingApp"). It is stored and surfaced via
// GET /devices; re-registration refreshes it as metadata.
func TestSDKRegisterAppName(t *testing.T) {
	srv := newCaptureTestAPI(t)

	// Seed + register with appName -> stored and returned.
	mustSeedDevice(t, srv, "com.example.integrating", "appname-dev")
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.integrating", Did: "appname-dev", AppName: "IntegratingApp"}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var list DeviceListResponse
	res := doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices", nil, &list)
	require.Equal(t, http.StatusOK, res.StatusCode)
	found := false
	for _, d := range list.Devices {
		if d.Did == "appname-dev" {
			found = true
			assert.Equal(t, "IntegratingApp", d.AppName, "appName must be stored and returned")
		}
	}
	require.True(t, found, "seeded device must appear in list")

	// Metadata refresh: registering again with a new appName updates it.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.integrating", Did: "appname-dev", AppName: "IntegratingApp Pro"}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	res = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices", nil, &list)
	require.Equal(t, http.StatusOK, res.StatusCode)
	for _, d := range list.Devices {
		if d.Did == "appname-dev" {
			assert.Equal(t, "IntegratingApp Pro", d.AppName, "appName refreshes as metadata on re-registration")
		}
	}

	// appName > 64 chars -> 400.
	var errResp ErrorResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register",
		RegisterDeviceRequest{App: "com.example.integrating", Did: "appname-dev", AppName: string(make([]byte, 65))}, &errResp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
