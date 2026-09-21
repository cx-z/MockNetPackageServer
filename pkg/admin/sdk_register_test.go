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
