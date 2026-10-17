package admin

// Real viewer-lease TTL sweep through the production health-check
// loop. Unlike the request-path tests in capture_handlers_test.go (which never
// start the background goroutine), this test runs StartHealthCheck with
// sub-second timing, keeps the DEVICE alive with heartbeats, and lets the
// VIEWER lease expire — proving the session is ended by lease expiry (not by
// the device-offline sweep) and that re-registering a viewer afterwards fails
// with 409 session_ended.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/stretchr/testify/require"
)

func TestCaptureHealthCheck_ViewerTTLExpiryEndsSession(t *testing.T) {
	const (
		app = "com.example.integrating" // neutral placeholder allowed by the test catalog
		did = "dev-ttl"
	)

	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithAPIKeyDisabled(),
		WithCaptureConfig(store.CaptureConfig{
			// Ticker fires every timeout/2 = 200ms; viewer leases live 300ms.
			HeartbeatInterval: 200 * time.Millisecond,
			HeartbeatTimeout:  400 * time.Millisecond,
			ViewerTTL:         300 * time.Millisecond,
		}),
	)
	srv := httptest.NewServer(api.httpServer.Handler)
	t.Cleanup(srv.Close)

	hcCtx, hcCancel := context.WithCancel(context.Background())
	api.captureManager.StartHealthCheck(hcCtx)
	t.Cleanup(func() {
		hcCancel()
		api.captureManager.Stop()
	})

	mustSeedDevice(t, srv, app, did)

	// Manual registration leaves LastSeenAt zero; one synchronous heartbeat
	// brings the device online before activation.
	prep := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/"+app+"/"+did+"/heartbeat",
		map[string]string{}, nil)
	require.Equal(t, http.StatusOK, prep.StatusCode)

	// Keep the device heartbeating for the whole test, so
	// any session end must be attributed to viewer-lease expiry rather than
	// the device-offline sweep.
	hbCtx, hbStop := context.WithCancel(context.Background())
	defer hbStop()
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				req, _ := http.NewRequest(http.MethodPost,
					srv.URL+"/api/v1/devices/"+app+"/"+did+"/heartbeat", bytes.NewReader([]byte("{}")))
				req.Header.Set("Content-Type", "application/json")
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
				}
			}
		}
	}()

	// Activate a capturing session.
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		map[string]string{"app": app, "did": did}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	require.Equal(t, capture.SessionStatusCapturing, session.Status)

	// Attach exactly one viewer lease and never renew it.
	var lease capture.ViewerLease
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/viewers",
		map[string]string{"viewerId": "v1", "label": "ttl-test"}, &lease)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// The sub-second test TTL (300ms) serializes as TTLSeconds=0; assert the
	// granted expiry directly instead.
	remaining := time.Until(lease.ExpiresAt)
	require.Greater(t, remaining, 200*time.Millisecond, "lease must carry the configured ~300ms TTL")
	require.LessOrEqual(t, remaining, 400*time.Millisecond)

	// Poll: the health check must end the session shortly after lease expiry
	// (lease 300ms + at most one 200ms tick => well under 3s on a loaded CI).
	deadline := time.Now().Add(3 * time.Second)
	var ended capture.CaptureSession
	for time.Now().Before(deadline) {
		var cur capture.CaptureSession
		r := doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID, nil, &cur)
		bodyOK := r.StatusCode == http.StatusOK
		r.Body.Close()
		if bodyOK && cur.Status == capture.SessionStatusEnded {
			ended = cur
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, capture.SessionStatusEnded, ended.Status,
		"session must be ended by viewer-lease expiry while the device stays online")
	require.NotNil(t, ended.RetainUntil, "ended session keeps a 48h retention deadline")

	// Registering/renewing a viewer on the ended session is now rejected.
	var apiErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	r := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/viewers",
		map[string]string{"viewerId": "v2"}, &apiErr)
	require.Equal(t, http.StatusConflict, r.StatusCode, "ended sessions reject new viewer leases")
	require.Equal(t, "session_ended", apiErr.Error)
}
