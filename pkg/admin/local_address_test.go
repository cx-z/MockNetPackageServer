package admin

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalAddress_RequiresAuth: the endpoint is Web-facing (login required).
func TestLocalAddress_RequiresAuth(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/local-address", "", nil)
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

// TestLocalAddress_OriginForLAN: authenticated request returns a phone-
// reachable origin: non-loopback host + the port the browser actually used.
// (Host-preservation with a custom Host like "localhost:4290" is covered by
// the pure TestRequestOrigin_* cases; here we go through the real HTTP path.)
func TestLocalAddress_OriginForLAN(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginToken(t, ts, "lannet", "secret123")

	var out LocalAddressResponse
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/local-address", tok, nil)
	require.Equal(t, http.StatusOK, res.StatusCode, "body=%s", body)
	require.NoError(t, json.Unmarshal(body, &out))

	u, err := url.Parse(out.Origin)
	require.NoError(t, err, "origin=%s", out.Origin)
	assert.Equal(t, "http", u.Scheme)
	assert.NotEmpty(t, u.Hostname(), "origin host must not be empty")
	assert.False(t, u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" ||
		u.Hostname() == "::1", "origin must not be loopback, got %s", out.Origin)
	assert.Equal(t, strconv.Itoa(ts.Listener.Addr().(*net.TCPAddr).Port), u.Port(), "origin must preserve the request port")
}

// TestRequestOrigin_PortPreserved / NoPort: the pure origin builder.
func TestRequestOrigin_PortPreserved(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://localhost:4290/x", nil)
	r.Host = "localhost:8080"
	assert.Equal(t, "http://192.168.1.3:8080", requestOrigin(r, "192.168.1.3"))
}

func TestRequestOrigin_NoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://localhost/x", nil)
	r.Host = "localhost"
	assert.Equal(t, "http://192.168.1.3", requestOrigin(r, "192.168.1.3"))
}

func TestRequestOrigin_HTTPS(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://localhost:443/x", nil)
	r.Host = "localhost:443"
	r.TLS = &tls.ConnectionState{}
	assert.Equal(t, "https://192.168.1.3:443", requestOrigin(r, "192.168.1.3"))
}
