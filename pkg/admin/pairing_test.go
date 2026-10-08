package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// issuePairingToken logs in as the given user and issues a pairing token for
// the app catalog's app, returning the API response.
func issuePairingToken(t *testing.T, ts *httptest.Server, username string) CreatePairingTokenResponse {
	t.Helper()
	tok := loginToken(t, ts, username, "secret123")
	var out CreatePairingTokenResponse
	res, body := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/pairing-tokens", tok,
		CreatePairingTokenRequest{App: "com.example.integrating"})
	require.Equal(t, http.StatusCreated, res.StatusCode, "issue token: body=%s", body)
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotEmpty(t, out.Token)
	return out
}

// listDevicesAs fetches the device list with the given user's token.
func listDevicesAs(t *testing.T, ts *httptest.Server, username string) DeviceListResponse {
	t.Helper()
	var list DeviceListResponse
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices",
		loginToken(t, ts, username, "secret123"), nil)
	require.Equal(t, http.StatusOK, res.StatusCode, "list devices: body=%s", body)
	require.NoError(t, json.Unmarshal(body, &list))
	return list
}

func TestPairingToken_IssueRequiresAuth(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/pairing-tokens", "",
		CreatePairingTokenRequest{App: "com.example.integrating"})
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestPairingToken_InvalidApp(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginToken(t, ts, "pairdev", "secret123")
	res, body := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/pairing-tokens", tok,
		CreatePairingTokenRequest{App: "com.not.in.catalog"})
	require.Equal(t, http.StatusBadRequest, res.StatusCode, "body=%s", body)
	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "invalid_app", errResp.Error)
}

func TestPairingToken_IssueAndRegister(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// TTL is ~10 minutes , token carries the app binding.
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), issue.ExpiresAt, time.Minute)

	// SDK register with the token: an unknown (app, did) is auto-registered
	// under the issuing user , with the supplied display name.
	var reg RegisterDeviceResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App:          "com.example.integrating",
		Did:          "scan-dev-1",
		DeviceName:   "办公 iPhone",
		PairingToken: issue.Token,
		Platform:     "ios",
		SDKVersion:   "0.3.0",
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NotNil(t, reg.Device)
	assert.Equal(t, "pairdev", reg.Device.Owner)
	assert.Equal(t, "办公 iPhone", reg.Device.Name)
	assert.Equal(t, "ios", string(reg.Device.Platform))

	// The registered device is visible to its owner in the Web list.
	list := listDevicesAs(t, ts, "pairdev")
	require.Len(t, list.Devices, 1)
	assert.Equal(t, "scan-dev-1", list.Devices[0].Did)
	assert.Equal(t, "pairdev", list.Devices[0].Owner)
}

func TestPairingToken_RegisterFallbackName(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// No deviceName -> platform + did prefix fallback.
	var reg RegisterDeviceResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App:          "com.example.integrating",
		Did:          "abcdef1234567890",
		Platform:     "ios",
		PairingToken: issue.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "ios·abcdef123456", reg.Device.Name)
}

func TestPairingToken_D7IdempotentReuse(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// First scan: creates the device with name "A".
	var reg RegisterDeviceResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-dev-2",
		DeviceName: "A", PairingToken: issue.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	firstRegisteredAt := reg.Device.RegisteredAt

	// Second scan of the same QR (same did): reused, not duplicated.
	res = doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-dev-2",
		DeviceName: "B", PairingToken: issue.Token, // different name on purpose
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.True(t, reg.Device.RegisteredAt.Equal(firstRegisteredAt), "re-register must keep RegisteredAt")
	assert.Equal(t, "A", reg.Device.Name, "reuse must never overwrite the device name ")
	assert.Equal(t, "pairdev", reg.Device.Owner)

	// A different user scans the same device: still reused, owner unchanged.
	other := issuePairingToken(t, ts, "otherdev")
	res = doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-dev-2",
		PairingToken: other.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "pairdev", reg.Device.Owner, "owner must not transfer on reuse ")

	// Device list still has exactly one record.
	list := listDevicesAs(t, ts, "pairdev")
	require.Len(t, list.Devices, 1)
}

func TestPairingToken_InvalidGarbageToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	var errResp ErrorResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-dev-x", PairingToken: "garbage-token",
	}, &errResp)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	assert.Equal(t, "pairing_token_invalid", errResp.Error)
}

func TestPairingToken_WrongAppRejected(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// Token minted for the catalog app; register claims another app -> 403.
	// handleRegisterDevice does not consult allowedApps (SDK channel), so the
	// app mismatch must be caught by ValidatePairingToken.
	var errResp ErrorResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.other.app", Did: "scan-dev-y", PairingToken: issue.Token,
	}, &errResp)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	assert.Equal(t, "pairing_token_invalid", errResp.Error)
}

func TestPairingToken_ExpiredRejected(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	// Forge an already-expired token directly in the store.
	expired := &account.PairingToken{
		Token:     "expired-token-abc",
		User:      "ghost",
		App:       "com.example.integrating",
		CreatedAt: time.Now().Add(-20 * time.Minute),
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, api.dataStore.PairingTokens().Create(t.Context(), expired))

	var errResp ErrorResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-dev-z", PairingToken: expired.Token,
	}, &errResp)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	assert.Equal(t, "pairing_token_invalid", errResp.Error)
}

func TestPairingToken_ReusableMultipleDevices(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// One QR (one token) onboards several devices  within the TTL.
	for _, did := range []string{"scan-a", "scan-b", "scan-c"} {
		var reg RegisterDeviceResponse
		res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
			App: "com.example.integrating", Did: did, PairingToken: issue.Token,
		}, &reg)
		require.Equal(t, http.StatusOK, res.StatusCode, "did=%s", did)
		require.NotNil(t, reg.Device)
		assert.Equal(t, "pairdev", reg.Device.Owner, "did=%s", did)
	}

	list := listDevicesAs(t, ts, "pairdev")
	require.Len(t, list.Devices, 3)
}

// TestPairingToken_StatusReflectsScan : after a device
// registers with the token, GET /pairing-tokens/{token} reports used=true and
// the paired device (did + name) — the Web polls this to auto-close the QR
// modal and open the naming flow.
func TestPairingToken_StatusReflectsScan(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// Before any registration: used=false, no paired devices.
	var status PairingTokenStatusResponse
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/pairing-tokens/"+issue.Token,
		loginToken(t, ts, "pairdev", "secret123"), nil)
	require.Equal(t, http.StatusOK, res.StatusCode, "body=%s", body)
	require.NoError(t, json.Unmarshal(body, &status))
	assert.False(t, status.Used)
	assert.Empty(t, status.PairedDevices)

	// Register a device with the token (simulating the SDK scan).
	var reg RegisterDeviceResponse
	rres := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App:          "com.example.integrating",
		Did:          "scan-dev-status-1",
		DeviceName:   "测试机",
		PairingToken: issue.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, rres.StatusCode)

	// Status now shows the paired device with its name.
	res2, body2 := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/pairing-tokens/"+issue.Token,
		loginToken(t, ts, "pairdev", "secret123"), nil)
	require.Equal(t, http.StatusOK, res2.StatusCode, "body=%s", body2)
	require.NoError(t, json.Unmarshal(body2, &status))
	assert.True(t, status.Used)
	require.Len(t, status.PairedDevices, 1)
	assert.Equal(t, "scan-dev-status-1", status.PairedDevices[0].Did)
	assert.Equal(t, "测试机", status.PairedDevices[0].Name)
	assert.False(t, status.PairedDevices[0].RegisteredAt.IsZero())
}

// TestPairingToken_StatusUnknownToken: unknown token -> 404.
func TestPairingToken_StatusUnknownToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginToken(t, ts, "pairdev", "secret123")
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/pairing-tokens/definitely-not-a-token", tok, nil)
	require.Equal(t, http.StatusNotFound, res.StatusCode, "body=%s", body)
	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "not_found", errResp.Error)
}

// TestPairingToken_AppNameRefreshedOnReuse: v0.9.0 — pairing registration
// stores the SDK-reported app display name; on D7 reuse the appName refreshes
// as metadata while name/owner stay untouched.
func TestPairingToken_AppNameRefreshedOnReuse(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	issue := issuePairingToken(t, ts, "pairdev")

	// First scan: create with name "dev" + appName "IntegratingApp".
	var reg RegisterDeviceResponse
	res := doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-appname", DeviceName: "dev",
		AppName: "IntegratingApp", PairingToken: issue.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "IntegratingApp", reg.Device.AppName)

	// Re-scan same did with a different appName + different device name:
	// appName refreshes, name/owner are preserved .
	res = doJSON(t, http.MethodPost, ts.URL+"/api/v1/devices/register", RegisterDeviceRequest{
		App: "com.example.integrating", Did: "scan-appname", DeviceName: "B",
		AppName: "IntegratingApp Pro", PairingToken: issue.Token,
	}, &reg)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "IntegratingApp Pro", reg.Device.AppName, "appName refreshes as metadata on reuse")
	assert.Equal(t, "dev", reg.Device.Name, "device name must not change on reuse ")
	assert.Equal(t, "pairdev", reg.Device.Owner)
}
