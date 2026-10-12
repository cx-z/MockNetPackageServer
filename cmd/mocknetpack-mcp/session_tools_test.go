package main

// Integration tests for the M12.8 capture-session tools. Unlike the fake-
// server handler tests in tools_test.go, these run against the REAL admin API
// (admin.NewAPI over a temp data dir, no-auth, real capture manager), so the
// MCP orchestration is verified against the genuine session/viewer-lease
// semantics: idempotent reuse, renew, reconnect, multi-observer isolation,
// last-viewer release side effects and error mapping.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getmockd/mockd/pkg/admin"
	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
	"github.com/getmockd/mockd/pkg/store"
)

const (
	testApp = "com.example.integrating"
	testDid = "dev-mcp"
	testKey = "test-key"
)

// ---- real backend harness -----------------------------------------------------

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startRealBackend boots the genuine admin API on a free port with a temp
// data dir and no API-key enforcement. The background health check runs
// (api.Start), exactly as in production.
func startRealBackend(t *testing.T, cfg store.CaptureConfig) string {
	t.Helper()
	port := freePort(t)
	api := admin.NewAPI(port,
		admin.WithDataDir(t.TempDir()),
		admin.WithAPIKeyDisabled(),
		admin.WithCaptureConfig(cfg),
	)
	require.NoError(t, api.Start())
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	t.Cleanup(func() {
		_ = api.Stop()
		time.Sleep(10 * time.Millisecond) // release data-dir handles before TempDir cleanup
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := http.Get(base + "/health"); err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("real backend on %s never became ready", base)
	return ""
}

func newMCPServer(base string) *server.MCPServer {
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)
	return srv
}

func callToolWithKey(t *testing.T, srv *server.MCPServer, key, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := srv.ListTools()[name]
	require.True(t, ok, "tool %q not registered", name)
	ctx := context.WithValue(context.Background(), ctxBearerKey, key)
	res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	require.NoError(t, err)
	return res
}

func callSessionTool(t *testing.T, srv *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	args = withDevice(args)
	return callToolWithKey(t, srv, testKey, name, args)
}

func withDevice(args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	args["app"] = testApp
	args["did"] = testDid
	return args
}

func mustResultJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "unexpected tool error: %s", resultText(res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(res)), &out))
	return out
}

func rawJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, url, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return res
}

// seedOnlineDevice manually registers a device and then refreshes its
// lastSeenAt via the SDK register endpoint so it is within the heartbeat
// timeout (eligible for session activation).
func seedOnlineDevice(t *testing.T, base string) {
	r := rawJSON(t, http.MethodPost, base+"/api/v1/devices",
		map[string]string{"app": testApp, "did": testDid, "name": "mcp-test"})
	require.Contains(t, []int{http.StatusCreated, http.StatusOK}, r.StatusCode)
	r.Body.Close()
	r = rawJSON(t, http.MethodPost, base+"/api/v1/devices/register",
		map[string]string{"app": testApp, "did": testDid, "platform": "ios", "sdkVersion": "test"})
	require.Equal(t, http.StatusOK, r.StatusCode)
	r.Body.Close()
}

func uploadOneEntry(t *testing.T, base, sid string) {
	entry := capture.TrafficEntry{
		Method: "GET", URL: "https://example.com/api/feed", Path: "/api/feed",
		StatusCode: 200, Timestamp: time.Now(),
	}
	r := rawJSON(t, http.MethodPost, base+"/api/v1/traffic", map[string]any{
		"app": testApp, "did": testDid, "sessionId": sid,
		"entries": []capture.TrafficEntry{entry},
	})
	require.Equal(t, http.StatusAccepted, r.StatusCode, "traffic upload must be accepted")
	r.Body.Close()
}

func enableFeedRule(t *testing.T, srv *server.MCPServer) string {
	res := callSessionTool(t, srv, "create_mock_rule", map[string]any{
		"method": "GET", "path": "/api/feed", "statusCode": float64(200),
		"body": `{"ok":true}`, "note": "m12.8 session test",
	})
	created := mustResultJSON(t, res)
	rule := created["rule"].(map[string]any)
	rid := rule["id"].(string)
	res = callSessionTool(t, srv, "set_mock_rule_enabled", map[string]any{
		"ruleId": rid, "enabled": true,
	})
	enabled := mustResultJSON(t, res)
	assert.Equal(t, true, enabled["enabled"])
	return rid
}

func standardBackend(t *testing.T) string {
	return startRealBackend(t, store.CaptureConfig{
		HeartbeatInterval: 20 * time.Second,
		HeartbeatTimeout:  60 * time.Second,
		ViewerTTL:         120 * time.Second,
	})
}

// ---- identity derivation (unit) ----------------------------------------------

func TestObserverIdentity_Derivation(t *testing.T) {
	a, err := deriveObserverIdentity("key-1", "", "")
	require.NoError(t, err)
	assert.Equal(t, defaultObserverID, a.observerID)
	assert.Equal(t, "mcp:default", a.label)
	assert.Regexp(t, `^mcp-[0-9a-f]{32}$`, a.viewerID)

	// Stable across reconnects.
	a2, err := deriveObserverIdentity("key-1", "default", "")
	require.NoError(t, err)
	assert.Equal(t, a.viewerID, a2.viewerID)

	// Different workflow ids on the same key => distinct leases.
	b, err := deriveObserverIdentity("key-1", "agent-2", "")
	require.NoError(t, err)
	assert.NotEqual(t, a.viewerID, b.viewerID)

	// Different keys never collide.
	c, err := deriveObserverIdentity("key-2", "default", "")
	require.NoError(t, err)
	assert.NotEqual(t, a.viewerID, c.viewerID)

	// Custom label is honored.
	d, err := deriveObserverIdentity("key-1", "default", "ci-job")
	require.NoError(t, err)
	assert.Equal(t, "ci-job", d.label)

	// Invalid observer ids are rejected (path-safe charset, 1-64 chars).
	// Note: an EMPTY observer id is not an error — it means the default.
	for _, bad := range []string{"has space", "slash/x", "中文", strings.Repeat("a", 65)} {
		_, err := deriveObserverIdentity("k", bad, "")
		assert.ErrorIs(t, err, errBadObserverID, "observerId %q must be rejected", bad)
	}
}

// ---- T2: reuse keeps traffic and rules ---------------------------------------

func TestSessionTools_StartReusePreservesTrafficAndRules(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	res := callSessionTool(t, srv, "start_capture", nil)
	first := mustResultJSON(t, res)
	assert.Equal(t, true, first["created"])
	session := first["session"].(map[string]any)
	sid := session["id"].(string)
	assert.NotEmpty(t, sid)

	// Traffic captured in the original session.
	uploadOneEntry(t, base, sid)

	// A rule created and enabled before reuse.
	rid := enableFeedRule(t, srv)

	// Start again: same session, created=false, untouched traffic and rules.
	res = callSessionTool(t, srv, "start_capture", nil)
	second := mustResultJSON(t, res)
	assert.Equal(t, false, second["created"])
	assert.Equal(t, true, second["reused"])
	assert.Equal(t, sid, second["session"].(map[string]any)["id"])

	// Traffic survived reuse.
	res = callSessionTool(t, srv, "get_device_traffic", map[string]any{"limit": float64(10)})
	traffic := mustResultJSON(t, res)
	assert.Equal(t, sid, traffic["sessionId"])
	entries := traffic["entries"].([]any)
	require.Len(t, entries, 1)
	assert.Equal(t, "/api/feed", entries[0].(map[string]any)["path"])

	// Rules survived reuse and stay enabled (activate must never disable).
	res = callSessionTool(t, srv, "list_mock_rules", map[string]any{})
	rules := mustResultJSON(t, res)["rules"].([]any)
	require.Len(t, rules, 1)
	got := rules[0].(map[string]any)
	assert.Equal(t, rid, got["ruleId"])
	assert.Equal(t, true, got["enabled"], "reuse must not disable existing rules")
	assert.Equal(t, true, got["effective"])
}

// ---- T3: renew refreshes TTL without adding viewers --------------------------

func TestSessionTools_RenewLease(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	start := mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
	sid := start["session"].(map[string]any)["id"].(string)
	firstLease := start["lease"].(map[string]any)
	assert.Equal(t, float64(120), firstLease["ttlSeconds"])
	firstExpiry, err := time.Parse(time.RFC3339Nano, firstLease["expiresAt"].(string))
	require.NoError(t, err)

	// Renew with the same (default) observer identity.
	res := callSessionTool(t, srv, "renew_capture_observer", nil)
	renewed := mustResultJSON(t, res)
	assert.Equal(t, sid, renewed["sessionId"])
	lease := renewed["lease"].(map[string]any)
	assert.Equal(t, float64(120), lease["ttlSeconds"])
	assert.Equal(t, float64(60), lease["recommendedRenewEverySeconds"])
	secondExpiry, err := time.Parse(time.RFC3339Nano, lease["expiresAt"].(string))
	require.NoError(t, err)
	assert.False(t, secondExpiry.Before(firstExpiry), "renewed expiry must not move backwards")

	// Viewer count stays at 1 (same derived viewer id).
	sessionView := renewed["session"].(map[string]any)
	assert.Equal(t, float64(1), sessionView["viewerCount"])
}

// ---- T4: ended session → 409 guidance, then recovery, traffic retained -------

func TestSessionTools_RenewAfterEndThenRecover(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	start := mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
	oldSID := start["session"].(map[string]any)["id"].(string)
	uploadOneEntry(t, base, oldSID)

	// Simulate the session ending (Web disconnect / forced end).
	r := rawJSON(t, http.MethodDelete, base+"/api/v1/sessions/"+oldSID, nil)
	require.Equal(t, http.StatusNoContent, r.StatusCode)
	r.Body.Close()

	// Implicit renew: no capturing session → actionable tool error.
	res := callSessionTool(t, srv, "renew_capture_observer", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "start_capture")

	// Explicit renew against the ended id → server 409 session_ended guidance.
	res = callSessionTool(t, srv, "renew_capture_observer", map[string]any{"sessionId": oldSID})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "抓包会话已结束")

	// start_capture recovers with a fresh session.
	res = callSessionTool(t, srv, "start_capture", nil)
	recovered := mustResultJSON(t, res)
	assert.Equal(t, true, recovered["created"])
	newSID := recovered["session"].(map[string]any)["id"].(string)
	assert.NotEqual(t, oldSID, newSID)

	// The old session's traffic remains queryable during the 48h window.
	gr := rawJSON(t, http.MethodGet, base+"/api/v1/sessions/"+oldSID+"/traffic?projection=compact", nil)
	require.Equal(t, http.StatusOK, gr.StatusCode)
	var oldTraffic struct {
		Entries []capture.TrafficEntry `json:"entries"`
		Total   int                    `json:"total"`
	}
	require.NoError(t, json.NewDecoder(gr.Body).Decode(&oldTraffic))
	gr.Body.Close()
	assert.Equal(t, 1, oldTraffic.Total, "ended session traffic must be retained")
}

// ---- T5: reconnect after gateway restart reuses the same lease ---------------

func TestSessionTools_ReconnectSameLease(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)

	// First gateway instance.
	srv1 := newMCPServer(base)
	first := mustResultJSON(t, callSessionTool(t, srv1, "start_capture", nil))
	sid := first["session"].(map[string]any)["id"].(string)
	viewer1 := first["observer"].(map[string]any)["viewerId"].(string)

	// A "restarted" gateway is a brand-new MCPServer; same key + default
	// observer must derive the same viewer id and NOT add a second viewer.
	srv2 := newMCPServer(base)
	again := mustResultJSON(t, callSessionTool(t, srv2, "start_capture", nil))
	assert.Equal(t, sid, again["session"].(map[string]any)["id"])
	assert.Equal(t, false, again["created"])
	viewer2 := again["observer"].(map[string]any)["viewerId"].(string)
	assert.Equal(t, viewer1, viewer2, "reconnect must reuse the derived lease")
	assert.Equal(t, float64(1), again["session"].(map[string]any)["viewerCount"])

	// A parallel workflow with a distinct observerId gets its own lease.
	parallel := mustResultJSON(t, callSessionTool(t, srv1, "start_capture",
		map[string]any{"observerId": "agent-2"}))
	assert.Equal(t, float64(2), parallel["session"].(map[string]any)["viewerCount"])
}

// ---- T6/T7: multi-observer isolation and idempotent release ------------------

func TestSessionTools_ReleaseIsolationAndIdempotency(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	start := mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
	sid := start["session"].(map[string]any)["id"].(string)
	mustResultJSON(t, callSessionTool(t, srv, "start_capture", map[string]any{"observerId": "w2"}))

	// Releasing a never-registered observer while others live is a harmless
	// no-op: the session stays capturing with its real viewers.
	res := callSessionTool(t, srv, "release_capture_observer", map[string]any{"observerId": "ghost"})
	noop := mustResultJSON(t, res)
	assert.Equal(t, true, noop["released"])
	assert.Equal(t, true, noop["idempotent"])
	assert.Equal(t, "capturing", noop["after"].(map[string]any)["status"])

	// Release the default observer: w2 keeps the session capturing.
	res = callSessionTool(t, srv, "release_capture_observer", nil)
	rel := mustResultJSON(t, res)
	assert.Equal(t, true, rel["released"])
	assert.Equal(t, false, rel["sessionEnded"])
	assert.Equal(t, "capturing", rel["after"].(map[string]any)["status"])
	assert.Equal(t, float64(1), rel["after"].(map[string]any)["viewerCount"])

	// Releasing default again is idempotent and still does not end anything.
	res = callSessionTool(t, srv, "release_capture_observer", nil)
	again := mustResultJSON(t, res)
	assert.Equal(t, true, again["idempotent"])
	assert.Equal(t, false, again["sessionEnded"])

	// Releasing the last observer (w2) ends the session.
	res = callSessionTool(t, srv, "release_capture_observer", map[string]any{"observerId": "w2"})
	last := mustResultJSON(t, res)
	assert.Equal(t, true, last["sessionEnded"])
	assert.Equal(t, true, last["rulesDisabledOnDevice"])
	assert.Equal(t, sid, last["sessionId"])
	assert.Equal(t, "ended", last["after"].(map[string]any)["status"])
	assert.NotNil(t, last["after"].(map[string]any)["retainUntil"])

	// Releasing on an already-ended session (explicit id) stays idempotent.
	res = callSessionTool(t, srv, "release_capture_observer",
		map[string]any{"observerId": "w2", "sessionId": sid})
	ended := mustResultJSON(t, res)
	assert.Equal(t, true, ended["released"])
	assert.Equal(t, true, ended["idempotent"])
	assert.Equal(t, false, ended["sessionEnded"])
}

// ---- T8: sole-observer guard + rule-disable side effect ----------------------

func TestSessionTools_KeepSessionWhenAloneGuard(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	rid := enableFeedRule(t, srv)
	mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))

	// Guarded release while solo: nothing is released, session and rules stay.
	res := callSessionTool(t, srv, "release_capture_observer",
		map[string]any{"keepSessionWhenAlone": true})
	skipped := mustResultJSON(t, res)
	assert.Equal(t, false, skipped["released"])
	assert.Equal(t, "sole_observer_session_would_end", skipped["reason"])

	rules := mustResultJSON(t, callSessionTool(t, srv, "list_mock_rules", nil))["rules"].([]any)
	assert.Equal(t, true, rules[0].(map[string]any)["enabled"], "guarded release must not touch rules")

	// Unguarded release ends the session and disables the device rules.
	res = callSessionTool(t, srv, "release_capture_observer", nil)
	last := mustResultJSON(t, res)
	assert.Equal(t, true, last["sessionEnded"])
	assert.Equal(t, true, last["rulesDisabledOnDevice"])
	traffic := last["traffic"].(map[string]any)
	assert.Equal(t, float64(48), traffic["retentionHours"])

	rules = mustResultJSON(t, callSessionTool(t, srv, "list_mock_rules", nil))["rules"].([]any)
	got := rules[0].(map[string]any)
	assert.Equal(t, rid, got["ruleId"])
	assert.Equal(t, false, got["enabled"], "session end disables rules (kept, not deleted)")
}

// ---- T9: offline / unregistered devices --------------------------------------

func TestSessionTools_StartOfflineAndUnregistered(t *testing.T) {
	// A backend with a 1s heartbeat timeout: the seeded device goes offline
	// quickly without SDK heartbeats.
	base := startRealBackend(t, store.CaptureConfig{
		HeartbeatInterval: time.Second,
		HeartbeatTimeout:  time.Second,
		ViewerTTL:         120 * time.Second,
	})
	seedOnlineDevice(t, base)
	time.Sleep(1500 * time.Millisecond)
	srv := newMCPServer(base)

	res := callSessionTool(t, srv, "start_capture", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "设备离线")

	// Unregistered coordinates → 404 guidance.
	res = callToolWithKey(t, srv, testKey, "start_capture", map[string]any{
		"app": "com.unknown", "did": "ghost",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "设备未注册")
}

// ---- T10: invalid observerId fails before touching the backend ---------------

func TestSessionTools_BadObserverIDRejected(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	res := callSessionTool(t, srv, "start_capture", map[string]any{"observerId": "bad id!"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "observerId")

	// No session must have been created by the rejected call.
	c := mnpapi.NewClient(base, testKey)
	sessions, err := c.ListSessions(context.Background(), testApp, testDid)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// ---- T11: status projection across idle / capturing / ended ------------------

func TestSessionTools_StatusProjection(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	// idle.
	res := callSessionTool(t, srv, "get_capture_status", nil)
	idle := mustResultJSON(t, res)
	assert.Equal(t, "idle", idle["device"].(map[string]any)["status"])
	assert.Nil(t, idle["session"])
	timeouts := idle["timeouts"].(map[string]any)
	assert.Equal(t, float64(60), timeouts["heartbeatTimeoutSeconds"])
	assert.Equal(t, float64(120), timeouts["observerLeaseTTLSeconds"])
	assert.Contains(t, idle["hint"].(string), "start_capture")

	// capturing.
	mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
	capturing := mustResultJSON(t, callSessionTool(t, srv, "get_capture_status", nil))
	assert.Equal(t, "capturing", capturing["device"].(map[string]any)["status"])
	sess := capturing["session"].(map[string]any)
	assert.Equal(t, "capturing", sess["status"])
	assert.Equal(t, float64(1), sess["viewerCount"])

	// ended → latestEndedSession carries retainUntil.
	mustResultJSON(t, callSessionTool(t, srv, "release_capture_observer", nil))
	ended := mustResultJSON(t, callSessionTool(t, srv, "get_capture_status", nil))
	assert.Nil(t, ended["session"])
	latest := ended["latestEndedSession"].(map[string]any)
	require.NotNil(t, latest, "latest ended session must be reported within retention")
	assert.Equal(t, "ended", latest["status"])
	assert.NotNil(t, latest["retainUntil"])
}

// ---- release with no active session is an idempotent success -----------------

func TestSessionTools_ReleaseWithoutSession(t *testing.T) {
	base := standardBackend(t)
	seedOnlineDevice(t, base)
	srv := newMCPServer(base)

	res := callSessionTool(t, srv, "release_capture_observer", nil)
	out := mustResultJSON(t, res)
	assert.Equal(t, true, out["released"])
	assert.Equal(t, true, out["idempotent"])
	assert.Equal(t, false, out["activeSession"])
}

// ---- missing coordinates are a tool-side validation error --------------------

func TestSessionTools_MissingAppDid(t *testing.T) {
	base := standardBackend(t)
	srv := newMCPServer(base)
	for _, name := range []string{"get_capture_status", "start_capture", "renew_capture_observer", "release_capture_observer"} {
		res := callToolWithKey(t, srv, testKey, name, map[string]any{})
		assert.True(t, res.IsError, "%s must require app/did", name)
		assert.Contains(t, resultText(res), "app 与 did")
	}
}
