package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getmockd/mockd/pkg/capture"
)

func TestKeyFromAuthorization(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		wantKey string
	}{
		{"missing header", "", ""},
		{"wrong scheme", "Token abc", ""},
		{"bearer with empty key", "Bearer ", ""},
		{"bearer lowercase", "bearer abc", ""}, // scheme is case-sensitive by convention (consistent with mockd)
		{"well-formed", "Bearer mnpk_abc123", "mnpk_abc123"},
		{"key with trailing spaces", "Bearer mnpk_abc ", "mnpk_abc "}, // extracted verbatim; validity enforced by mockd
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			assert.Equal(t, tc.wantKey, keyFromAuthorization(r))
		})
	}
}

func TestAuthGateway(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	gw := authGateway(next)

	t.Run("non-mcp path returns 404", func(t *testing.T) {
		called = false
		r := httptest.NewRequest(http.MethodGet, "/other", nil)
		r.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.False(t, called)
	})

	t.Run("mcp without header returns 401", func(t *testing.T) {
		called = false
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, called)
	})

	t.Run("mcp with malformed header returns 401", func(t *testing.T) {
		called = false
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.Header.Set("Authorization", "Token abc")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, called)
	})

	t.Run("mcp with well-formed header passes through", func(t *testing.T) {
		called = false
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, called)
	})
}

// TestMCPHandler_SendsCallerKeyAsAuthorization asserts the passthrough plumbing:
// a handler invoked with ctxBearerKey in the context must present exactly that
// key as Authorization to the mockd admin API.
func TestMCPHandler_SendsCallerKeyAsAuthorization(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		writeResp(w, 200, map[string]any{
			"devices": []capture.DeviceView{{Device: &capture.Device{App: "com.a", Did: "d1", Name: "n"}, Status: capture.DeviceStatusCapturing}},
			"total":   1,
		})
	})
	f, base := setupFake(t, mux)
	require.NotNil(t, f)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)
	res := callTool(t, srv, "list_devices", nil)
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), "d1")
}
