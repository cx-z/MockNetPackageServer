package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionTools_InvalidIdentityMakesNoBackendRequests exercises validation
// through every lease handler, including padded IDs that used to alias default.
func TestSessionTools_InvalidIdentityMakesNoBackendRequests(t *testing.T) {
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)
	srv := newMCPServer(backend.URL)
	for _, name := range []string{"start_capture", "renew_capture_observer", "release_capture_observer"} {
		for _, bad := range []any{" default ", "default ", " default", " ", "\t", "\ndefault", strings.Repeat("a", 65), float64(1), true, nil} {
			res := callSessionTool(t, srv, name, map[string]any{"observerId": bad})
			require.True(t, res.IsError, "%s accepted %#v", name, bad)
			assert.Contains(t, resultText(res), "observerId")
		}
	}
	res := callSessionTool(t, srv, "start_capture", map[string]any{"label": strings.Repeat("注", 65)})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "label")
	assert.Zero(t, requests.Load(), "invalid identities must be rejected before HTTP")
}

// TestSessionTools_PaddedReleasePreservesDefaultLease proves that invalid
// input cannot release an already registered valid default observer.
func TestSessionTools_PaddedReleasePreservesDefaultLease(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)
	start := mustResultJSON(t, callSessionTool(t, srv, "start_capture", map[string]any{"observerId": ""}))
	assert.Equal(t, false, start["sessionSnapshotStale"])
	for _, bad := range []string{" default ", " "} {
		res := callSessionTool(t, srv, "release_capture_observer", map[string]any{"observerId": bad})
		require.True(t, res.IsError)
	}
	status := mustResultJSON(t, callSessionTool(t, srv, "get_capture_status", nil))
	assert.Equal(t, float64(1), status["session"].(map[string]any)["viewerCount"])
	assert.Equal(t, "capturing", status["session"].(map[string]any)["status"])
}

// TestObserverIdentity_LabelLimits keeps all approved observer IDs valid while
// enforcing the 64-character limit only for custom labels (Unicode characters).
func TestObserverIdentity_LabelLimits(t *testing.T) {
	id, err := deriveObserverIdentity(testKey, strings.Repeat("a", 64), "")
	require.NoError(t, err)
	assert.Len(t, id.label, 68)
	id, err = deriveObserverIdentity(testKey, "default", strings.Repeat("注", 64))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("注", 64), id.label)
	_, err = deriveObserverIdentity(testKey, "default", strings.Repeat("注", 65))
	require.Error(t, err)
}

// TestSessionTools_StartRefreshFailureDisclosesStaleSnapshot injects a failed
// post-registration GET, without discarding the successfully acquired lease.
func TestSessionTools_StartRefreshFailureDisclosesStaleSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeResp(w, http.StatusCreated, capture.CaptureSession{ID: "s1", Status: capture.SessionStatusCapturing})
	})
	id, err := deriveObserverIdentity(testKey, "default", "")
	require.NoError(t, err)
	mux.HandleFunc("POST /api/v1/sessions/s1/viewers", func(w http.ResponseWriter, _ *http.Request) {
		writeResp(w, http.StatusOK, capture.ViewerLease{ViewerID: id.viewerID, TTLSeconds: 120, ExpiresAt: time.Now().Add(120 * time.Second)})
	})
	mux.HandleFunc("GET /api/v1/sessions/s1", func(w http.ResponseWriter, _ *http.Request) {
		writeResp(w, http.StatusInternalServerError, map[string]string{"code": "internal_error", "message": "refresh failed"})
	})
	_, base := setupFake(t, mux)
	out := mustResultJSON(t, callSessionTool(t, newMCPServer(base), "start_capture", nil))
	assert.Equal(t, true, out["sessionSnapshotStale"])
	assert.Contains(t, out["warning"], "登记前快照")
	assert.Contains(t, out["hint"], "get_capture_status")
	assert.Equal(t, "s1", out["session"].(map[string]any)["id"])
	assert.Equal(t, id.viewerID, out["lease"].(map[string]any)["viewerId"])
	assert.Equal(t, float64(120), out["lease"].(map[string]any)["ttlSeconds"])
}

// TestSessionTools_KeepGuardConcurrentDeparture records the documented
// snapshot-only boundary using real admin endpoints: another viewer departs
// after the guard's two-viewer snapshot, so our release still ends the session.
func TestSessionTools_KeepGuardConcurrentDeparture(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)
	first := mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
	sid := first["session"].(map[string]any)["id"].(string)
	mustResultJSON(t, callSessionTool(t, srv, "start_capture", map[string]any{"observerId": "other"}))
	other, err := deriveObserverIdentity(testKey, "other", "")
	require.NoError(t, err)
	upstream, err := url.Parse(base)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var intercepted atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/"+sid && intercepted.CompareAndSwap(false, true) {
			client := mnpapi.NewClient(base, testKey)
			snapshot, getErr := client.GetSession(context.Background(), sid)
			if getErr != nil {
				t.Error(getErr)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if releaseErr := client.ReleaseViewer(context.Background(), sid, other.viewerID); releaseErr != nil {
				t.Error(releaseErr)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeResp(w, http.StatusOK, snapshot)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)
	guardTool := toolReleaseCaptureObserver()
	assert.Contains(t, guardTool.Description, "不能保证持续抓包")
	out := mustResultJSON(t, callSessionTool(t, newMCPServer(backend.URL), "release_capture_observer",
		map[string]any{"sessionId": sid, "keepSessionWhenAlone": true}))
	assert.Equal(t, float64(2), out["before"].(map[string]any)["viewerCount"])
	assert.Equal(t, true, out["released"])
	assert.Equal(t, true, out["sessionEnded"])
	assert.Equal(t, float64(0), out["after"].(map[string]any)["viewerCount"])
}
