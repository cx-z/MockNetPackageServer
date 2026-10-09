package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTrafficSession seeds a device + active session and uploads entries.
func seedTrafficSession(t *testing.T, srv *httptest.Server, entries []*capture.TrafficEntry) string {
	t.Helper()
	mustSeedDevice(t, srv, "com.example.integrating", "dev-m12")
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.integrating", Did: "dev-m12"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var up TrafficUploadResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "com.example.integrating", Did: "dev-m12", SessionID: session.ID, Entries: entries}, &up)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.Equal(t, len(entries), up.Count)
	return session.ID
}

func m12Entry(method, url string, ts time.Time) *capture.TrafficEntry {
	return &capture.TrafficEntry{
		Timestamp:       ts,
		Method:          method,
		URL:             url,
		Path:            "/users",
		Query:           "k=v",
		RequestHeaders:  map[string][]string{"X-Test": {"1"}},
		RequestBody:     `{"a":1}`,
		StatusCode:      200,
		ResponseBody:    `{"ok":true}`,
		ResponseHeaders: map[string][]string{"Content-Type": {"application/json"}},
		DurationMs:      12,
		Mocked:          true,
	}
}

// TestTrafficCompactProjection asserts S4: ?projection=compact returns
// exactly the machine-facing field set, and the default (no projection)
// response still carries the full detail fields — the Web never changes shape.
func TestTrafficCompactProjection(t *testing.T) {
	srv := newCaptureTestAPI(t)
	now := time.Now()
	sessionID := seedTrafficSession(t, srv, []*capture.TrafficEntry{
		m12Entry("GET", "https://api.example.com/users?id=1", now),
	})

	var compact TrafficCompactListResponse
	resp := doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact", nil, &compact)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, compact.Entries, 1)
	e := compact.Entries[0]
	assert.NotEmpty(t, e.ID, "compact keeps id")
	assert.Equal(t, int64(1), e.Seq, "compact carries the seq cursor")
	assert.Equal(t, "GET", e.Method)
	assert.Equal(t, "https://api.example.com/users?id=1", e.URL)
	assert.Equal(t, "/users", e.Path)
	assert.Equal(t, 200, e.StatusCode)
	assert.Equal(t, 12, e.DurationMs)
	assert.True(t, e.Mocked)

	// The compact body must not carry the heavy Web fields.
	raw := marshalRaw(t, compact)
	assert.NotContains(t, raw, "requestHeaders")
	assert.NotContains(t, raw, "requestBody")
	assert.NotContains(t, raw, "responseBody")
	assert.NotContains(t, raw, "sessionId")

	// Default projection: full entry shape unchanged (S4 non-regression).
	var full TrafficListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/traffic", nil, &full)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, full.Entries, 1)
	fe := full.Entries[0]
	assert.Equal(t, `{"a":1}`, fe.RequestBody)
	assert.Equal(t, `{"ok":true}`, fe.ResponseBody)
	assert.NotEmpty(t, fe.RequestHeaders)
	assert.Equal(t, "k=v", fe.Query)
	assert.Equal(t, sessionID, fe.SessionID)
}

// TestTrafficSinceIncremental asserts S5: since=<seq> returns only entries
// newer than the cursor, repeated pulls are idempotent, and seq advances
// across batches (arrival order).
func TestTrafficSinceIncremental(t *testing.T) {
	srv := newCaptureTestAPI(t)
	now := time.Now()
	sessionID := seedTrafficSession(t, srv, []*capture.TrafficEntry{
		m12Entry("GET", "https://a.example.com/1", now),
		m12Entry("POST", "https://a.example.com/2", now.Add(time.Millisecond)),
	})

	// First pull: seq 1..2, remember the max seq.
	var page TrafficCompactListResponse
	resp := doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact&limit=500", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, page.Entries, 2)
	require.Equal(t, []int64{1, 2}, []int64{page.Entries[0].Seq, page.Entries[1].Seq})

	// Upload batch 2 (arrival later).
	var up TrafficUploadResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic",
		TrafficUploadRequest{App: "com.example.integrating", Did: "dev-m12", SessionID: sessionID,
			Entries: []*capture.TrafficEntry{m12Entry("GET", "https://a.example.com/3", now.Add(2*time.Millisecond))}}, &up)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// since=2 → exactly the new entry.
	resp = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact&since=2", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, page.Total)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, int64(3), page.Entries[0].Seq)
	assert.Contains(t, page.Entries[0].URL, "/3")

	// Idempotent re-pull of the same cursor.
	resp = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact&since=2", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, page.Total)

	// since=0 / absent → everything (3 entries).
	resp = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact&since=0&limit=500", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 3, page.Total)
}

// TestTrafficMethodSchemeFilter asserts S6: method / scheme narrow the list
// server-side and AND with the existing filters.
func TestTrafficMethodSchemeFilter(t *testing.T) {
	srv := newCaptureTestAPI(t)
	now := time.Now()
	sessionID := seedTrafficSession(t, srv, []*capture.TrafficEntry{
		m12Entry("GET", "https://api.example.com/users", now),
		m12Entry("POST", "http://api.example.com/users", now.Add(time.Millisecond)),
		m12Entry("POST", "https://api.example.com/orders", now.Add(2*time.Millisecond)),
	})

	get := func(q string) TrafficCompactListResponse {
		var page TrafficCompactListResponse
		resp := doJSON(t, http.MethodGet,
			srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=compact&limit=500"+q, nil, &page)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return page
	}

	// method=POST (exact, case-insensitive match on the server side).
	assert.Equal(t, 2, get("&method=POST").Total)
	assert.Equal(t, 2, get("&method=post").Total, "lowercase method is forgiven")
	assert.Equal(t, 1, get("&method=GET").Total)

	// scheme=https / http (URL prefix parse).
	assert.Equal(t, 2, get("&scheme=https").Total)
	assert.Equal(t, 1, get("&scheme=http").Total)

	// AND combination.
	assert.Equal(t, 1, get("&method=POST&scheme=https").Total)
	assert.Equal(t, 0, get("&method=GET&scheme=http").Total)

	// AND with the existing keyword filter.
	assert.Equal(t, 1, get("&method=POST&scheme=https&keyword=orders").Total)
}

// TestTrafficQueryInvalidParams asserts invalid projection / since are
// rejected with 400 rather than silently ignored.
func TestTrafficQueryInvalidParams(t *testing.T) {
	srv := newCaptureTestAPI(t)
	now := time.Now()
	sessionID := seedTrafficSession(t, srv, []*capture.TrafficEntry{
		m12Entry("GET", "https://api.example.com/users", now),
	})

	resp := doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?projection=full", nil, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?since=-1", nil, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID+"/traffic?since=abc", nil, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// marshalRaw renders the value back to JSON for substring assertions.
func marshalRaw(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
