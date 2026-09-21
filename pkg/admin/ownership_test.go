package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshDevToken registers a fresh dev user and returns its bearer token.
func freshDevToken(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	return loginToken(t, ts, name, "secret123")
}

func parseDeviceList(t *testing.T, body []byte) []struct {
	Did   string `json:"did"`
	Owner string `json:"owner"`
} {
	t.Helper()
	var resp struct {
		Devices []struct {
			Did   string `json:"did"`
			Owner string `json:"owner"`
		} `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp.Devices
}

// TestOwnership_ListFiltersByOwner: a dev sees only its own devices; admin sees all.
func TestOwnership_ListFiltersByOwner(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	devA := freshDevToken(t, ts, "ownA")
	devB := freshDevToken(t, ts, "ownB")

	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", devA,
		map[string]string{"app": "com.example.integrating", "did": "devA-dev", "name": "A的设备"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	res, _ = doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", devB,
		map[string]string{"app": "com.example.integrating", "did": "devB-dev", "name": "B的设备"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	_, bodyA := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", devA, nil)
	devsA := parseDeviceList(t, bodyA)
	require.Len(t, devsA, 1)
	assert.Equal(t, "devA-dev", devsA[0].Did)

	_, bodyB := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", devB, nil)
	devsB := parseDeviceList(t, bodyB)
	require.Len(t, devsB, 1)
	assert.Equal(t, "devB-dev", devsB[0].Did)
}

// TestOwnership_CrossDeviceIs404: devB cannot see/touch devA's device by URL.
func TestOwnership_CrossDeviceIs404(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	devA := freshDevToken(t, ts, "crossA")
	devB := freshDevToken(t, ts, "crossB")

	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", devA,
		map[string]string{"app": "com.example.integrating", "did": "crossA-dev", "name": "A"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	res, _ = doAuthJSON(t, http.MethodGet,
		ts.URL+"/api/v1/devices/com.example.integrating/crossA-dev", devB, nil)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)

	res, _ = doAuthJSON(t, http.MethodPut,
		ts.URL+"/api/v1/devices/com.example.integrating/crossA-dev", devB,
		map[string]string{"name": "hacked"})
	assert.Equal(t, http.StatusNotFound, res.StatusCode)

	res, _ = doAuthJSON(t, http.MethodGet,
		ts.URL+"/api/v1/devices/com.example.integrating/crossA-dev/mock-rules", devB, nil)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)

	res, _ = doAuthJSON(t, http.MethodPut,
		ts.URL+"/api/v1/devices/com.example.integrating/crossA-dev", devA,
		map[string]string{"name": "A改名"})
	require.Equal(t, http.StatusOK, res.StatusCode)
}

// TestOwnership_AdminSeesAll: admin list includes every dev's device.
func TestOwnership_AdminSeesAll(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	require.NoError(t, api.CreateAdminUser(t.Context(), "root", "admin-secret"))

	for _, name := range []string{"adminDev1", "adminDev2"} {
		tok := freshDevToken(t, ts, name)
		res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok,
			map[string]string{"app": "com.example.integrating", "did": name + "-dev", "name": name})
		require.Equal(t, http.StatusCreated, res.StatusCode)
	}
	_, body := loginUser(t, ts, "root", "admin-secret")
	var lr LoginAuthResponse
	require.NoError(t, json.Unmarshal(body, &lr))

	_, listBody := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", lr.Token, nil)
	devs := parseDeviceList(t, listBody)
	assert.GreaterOrEqual(t, len(devs), 2)
}
