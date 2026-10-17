package mnpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServer is a scriptable mocknetpack admin API for client tests.
type fakeServer struct {
	ts       *httptest.Server
	mu       sync.Mutex
	handler  http.HandlerFunc
	lastPath string
	lastAuth string
	queries  []string
	lastBody string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.lastPath = r.URL.Path
	f.lastAuth = r.Header.Get("Authorization")
	f.queries = append(f.queries, r.URL.RawQuery)
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.lastBody = string(body)
	f.mu.Unlock()
	f.handler(w, r)
}

func (f *fakeServer) hasQuery(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, q := range f.queries {
		if strings.Contains(q, sub) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func newTestClient(t *testing.T, f *fakeServer) *Client {
	t.Helper()
	return NewClient(f.ts.URL, "test-key")
}

func TestClient_ListDevices(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"devices": []capture.DeviceView{{Device: &capture.Device{App: "com.a", Did: "d1", Name: "n1"}}},
			"total":   1,
		})
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	devices, err := c.ListDevices(context.Background())
	require.NoError(t, err)
	require.Len(t, devices, 1)
	assert.Equal(t, "com.a", devices[0].App)
	assert.Equal(t, "Bearer test-key", f.lastAuth, "client must send the API key")
}

func TestClient_GetDeviceTraffic_TwoStepOrchestration(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/sessions":
			writeJSON(w, 200, map[string]any{
				"sessions": []capture.CaptureSession{
					{ID: "s-old", Status: capture.SessionStatusEnded},
					{ID: "s-active", Status: capture.SessionStatusCapturing},
				},
				"total": 2,
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/sessions/s-active/traffic"):
			writeJSON(w, 200, map[string]any{
				"entries": []capture.TrafficEntry{{ID: "t1", Method: "GET", URL: "https://x.com/a", Seq: 5}},
				"total":   1,
			})
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	res, err := c.GetDeviceTraffic(context.Background(), "com.a", "d1", TrafficQuery{Compact: true, Method: "GET", Scheme: "https", Since: 3, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, "s-active", res.SessionID, "capturing session wins over ended")
	require.Len(t, res.Entries, 1)
	assert.Equal(t, int64(5), res.Entries[0].Seq)
	assert.Contains(t, f.lastPath, "/sessions/s-active/traffic")
	assert.True(t, f.hasQuery("projection=compact"), "traffic query must carry projection=compact")
	assert.True(t, f.hasQuery("method=GET"), "traffic query must carry method=GET")
	assert.True(t, f.hasQuery("scheme=https"), "traffic query must carry scheme=https")
	assert.True(t, f.hasQuery("since=3"), "traffic query must carry since=3")
	assert.True(t, f.hasQuery("limit=10"), "traffic query must carry limit=10")
	assert.True(t, f.hasQuery("app=com.a"), "sessions query must carry app=com.a")
	assert.True(t, f.hasQuery("did=d1"), "sessions query must carry did=d1")
}

func TestClient_GetDeviceTraffic_NoSession(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"sessions": []capture.CaptureSession{}, "total": 0})
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	res, err := c.GetDeviceTraffic(context.Background(), "com.a", "d1", TrafficQuery{})
	require.NoError(t, err)
	assert.Equal(t, "", res.SessionID)
	assert.Empty(t, res.Entries)
}

func TestClient_CreateMockRuleFromTraffic(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/traffic/t1":
			writeJSON(w, 200, capture.TrafficEntry{
				ID: "t1", Method: "POST", Path: "/api/feed", URL: "https://a.com/api/feed?x=1",
				Query: "x=1", StatusCode: 201, DurationMs: 5,
				RequestHeaders:      map[string][]string{"Content-Type": {"application/json"}},
				RequestBody:         `{"a":1}`,
				RequestBodyBase64:   "AAEC",
				RequestBodyDecoded:  `{"h_ch":"appstore"}`,
				ResponseHeaders:     map[string][]string{"X-Server": {"mockd"}},
				ResponseBody:        `{"ok":true}`,
				ResponseBodyBase64:  "AwQF",
				ResponseBodyDecoded: `{"ret":1}`,
				Timestamp:           time.Now(),
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/devices/com.a/d1/mock-rules"):
			var in capture.MockRuleInput
			require.NoError(t, json.Unmarshal([]byte(f.lastBody), &in))
			assert.Equal(t, "POST", in.Method)
			assert.Equal(t, "/api/feed", in.Path)
			assert.Equal(t, 201, in.Response.StatusCode)
			assert.Equal(t, `{"ok":true}`, in.Response.Body)
			assert.Equal(t, "mockd", in.Response.Headers["X-Server"])
			require.NotNil(t, in.Source)
			assert.Equal(t, "https://a.com/api/feed?x=1", in.Source.URL)
			assert.Equal(t, "my note", in.Note)
			// : the frozen source must carry the app-decoded bodies exactly
			// like the Web "Mock 此请求" path, so the rule detail view can show
			// the decoded request/response JSON of binary (xcp) traffic.
			assert.Equal(t, `{"h_ch":"appstore"}`, in.Source.RequestBodyDecoded)
			assert.Equal(t, `{"ret":1}`, in.Source.ResponseBodyDecoded)
			assert.Equal(t, "AAEC", in.Source.RequestBodyBase64)
			assert.Equal(t, "AwQF", in.Source.ResponseBodyBase64)
			writeJSON(w, 201, capture.MockRuleView{MockRule: &capture.MockRule{ID: "r1", Method: "POST", Path: "/api/feed"}})
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	rule, err := c.CreateMockRuleFromTraffic(context.Background(), "com.a", "d1", "t1", "my note")
	require.NoError(t, err)
	assert.Equal(t, "r1", rule.ID)
}

func TestClient_UpdateAndSetEnabled(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/devices/com.a/d1/mock-rules"):
			if r.Method == http.MethodGet {
				writeJSON(w, 200, map[string]any{
					"version": 2,
					"rules":   []capture.MockRuleView{{MockRule: &capture.MockRule{ID: "r1", Method: "GET", Path: "/h", Response: capture.MockResponse{StatusCode: 200, Body: "old"}}, Effective: true}},
				})
				return
			}
			var in capture.UpdateMockRuleInput
			require.NoError(t, json.Unmarshal([]byte(f.lastBody), &in))
			assert.Equal(t, "new-body", in.Response.Body)
			writeJSON(w, 200, capture.MockRuleView{MockRule: &capture.MockRule{ID: "r1", Enabled: true}})
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	enabled := true
	rule, err := c.UpdateMockRule(context.Background(), "com.a", "d1", "r1", &capture.UpdateMockRuleInput{
		Response: capture.MockResponse{StatusCode: 200, Body: "new-body"}, Note: "n", Enabled: &enabled,
	})
	require.NoError(t, err)
	assert.True(t, rule.Enabled)

	// GetMockRule resolves through the list.
	got, err := c.GetMockRule(context.Background(), "com.a", "d1", "r1")
	require.NoError(t, err)
	assert.Equal(t, "old", got.Response.Body)
}

// TestClient_ErrorGuidance asserts : 401/403/404/409 surface with the
// AI-facing guidance text (给 AI 的指引).
func TestClient_ErrorGuidance(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		msg    string
		want   string
	}{
		{"unauthorized", 401, "unauthorized", "missing or invalid bearer token", "MOCKNETPACK_API_KEY"},
		{"forbidden", 403, "forbidden", "仅规则 owner 或 admin 可编辑", "owner 或 admin"},
		{"not_found", 404, "not_found", "规则不存在", "先查询确认 id 有效"},
		{"conflict", 409, "rule_conflict", "同接口已存在生效规则", "互斥冲突"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
				writeErr(w, tc.status, tc.code, tc.msg)
			}}
			f.ts = httptest.NewServer(f)
			defer f.ts.Close()

			c := newTestClient(t, f)
			_, err := c.ListDevices(context.Background())
			require.Error(t, err)
			var ae *APIError
			require.True(t, errors.As(err, &ae), "want *APIError, got %T", err)
			assert.Equal(t, tc.status, ae.Status)
			assert.Equal(t, tc.code, ae.Code)
			assert.Contains(t, ae.Message, tc.want, "guidance text missing")
		})
	}
}

func TestClient_NetworkFailure(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "k") // nothing listens here
	_, err := c.ListDevices(context.Background())
	require.Error(t, err)
	var ae *APIError
	require.False(t, errors.As(err, &ae), "network errors must not masquerade as API errors")
}

func TestClient_ShareRoundTrip(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/shares" && r.Method == http.MethodPost:
			writeJSON(w, 201, ShareCreated{ShareID: "s1", URL: "/mocknetpack/#/share/s1", ExpiresAt: time.Now().Add(7 * 24 * time.Hour)})
		case r.URL.Path == "/api/v1/shares/s1":
			writeJSON(w, 200, ShareSnapshot{ShareID: "s1", Entry: &capture.TrafficEntry{ID: "t1", Method: "GET"}})
		default:
			writeErr(w, 404, "not_found", fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path))
		}
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()

	c := newTestClient(t, f)
	created, err := c.CreateShare(context.Background(), "t1")
	require.NoError(t, err)
	assert.Equal(t, "s1", created.ShareID)

	snap, err := c.GetShare(context.Background(), "s1")
	require.NoError(t, err)
	assert.Equal(t, "GET", snap.Entry.Method)
}

// ---- session/viewer client methods -------------------------------------

func TestClient_ActivateAndReuseSession(t *testing.T) {
	var activations int
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/sessions" && r.Method == http.MethodPost:
			activations++
			status := http.StatusOK
			if activations == 1 {
				status = http.StatusCreated
			}
			writeJSON(w, status, capture.CaptureSession{ID: "S1", App: "a", Did: "d", Status: capture.SessionStatusCapturing})
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()
	c := newTestClient(t, f)

	s1, created, err := c.ActivateSession(context.Background(), "a", "d")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "S1", s1.ID)

	s2, created, err := c.ActivateSession(context.Background(), "a", "d")
	require.NoError(t, err)
	assert.False(t, created, "repeated activation must report created=false (reuse)")
	assert.Equal(t, "S1", s2.ID)
	assert.Contains(t, f.lastBody, `"app":"a"`)
	assert.Contains(t, f.lastBody, `"did":"d"`)
}

func TestClient_ViewerRegisterRenewRelease(t *testing.T) {
	var posts int
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/sessions/S1/viewers" && r.Method == http.MethodPost:
			posts++
			writeJSON(w, http.StatusOK, capture.ViewerLease{
				ViewerID: "V1", TTLSeconds: 120, ExpiresAt: time.Now().Add(120 * time.Second),
			})
		case r.URL.Path == "/api/v1/sessions/S1/viewers/V1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()
	c := newTestClient(t, f)

	lease, err := c.RegisterViewer(context.Background(), "S1", "V1", "mcp:default")
	require.NoError(t, err)
	assert.Equal(t, 120, lease.TTLSeconds)
	assert.Contains(t, f.lastBody, `"viewerId":"V1"`)
	assert.Contains(t, f.lastBody, `"label":"mcp:default"`)

	// Re-posting the same viewer is the renew call (same endpoint/body).
	_, err = c.RegisterViewer(context.Background(), "S1", "V1", "mcp:default")
	require.NoError(t, err)
	assert.Equal(t, 2, posts)

	// Release hits the per-viewer DELETE path and 204 is a clean nil error.
	require.NoError(t, c.ReleaseViewer(context.Background(), "S1", "V1"))
	assert.Equal(t, "/api/v1/sessions/S1/viewers/V1", f.lastPath)
}

func TestClient_SessionReadsAndCurrentCapturing(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/devices/a/d" && r.Method == http.MethodGet:
			writeJSON(w, 200, capture.DeviceView{
				Device: &capture.Device{App: "a", Did: "d"},
				Status: capture.DeviceStatusCapturing,
			})
		case r.URL.Path == "/api/v1/sessions" && r.Method == http.MethodGet:
			assert.Equal(t, "a", r.URL.Query().Get("app"))
			assert.Equal(t, "d", r.URL.Query().Get("did"))
			// Newest first: an ended session precedes the capturing one.
			writeJSON(w, 200, map[string]any{
				"total": 2,
				"sessions": []capture.CaptureSession{
					{ID: "Sold", Status: capture.SessionStatusEnded},
					{ID: "Snew", Status: capture.SessionStatusCapturing},
				},
			})
		case r.URL.Path == "/api/v1/sessions/Snew" && r.Method == http.MethodGet:
			writeJSON(w, 200, capture.CaptureSession{ID: "Snew", Status: capture.SessionStatusCapturing, ViewerCount: 1})
		default:
			writeErr(w, 404, "not_found", "unexpected "+r.URL.Path)
		}
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()
	c := newTestClient(t, f)

	dv, err := c.GetDevice(context.Background(), "a", "d")
	require.NoError(t, err)
	assert.Equal(t, capture.DeviceStatusCapturing, dv.Status)

	sessions, err := c.ListSessions(context.Background(), "a", "d")
	require.NoError(t, err)
	assert.Len(t, sessions, 2)

	cur, err := c.CurrentCapturingSession(context.Background(), "a", "d")
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, "Snew", cur.ID, "must pick the capturing session regardless of order")

	one, err := c.GetSession(context.Background(), "Snew")
	require.NoError(t, err)
	assert.Equal(t, 1, one.ViewerCount)
}

func TestClient_CurrentCapturingSession_None(t *testing.T) {
	f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"total": 1,
			"sessions": []capture.CaptureSession{
				{ID: "Sold", Status: capture.SessionStatusEnded},
			},
		})
	}}
	f.ts = httptest.NewServer(f)
	defer f.ts.Close()
	c := newTestClient(t, f)

	cur, err := c.CurrentCapturingSession(context.Background(), "a", "d")
	require.NoError(t, err)
	assert.Nil(t, cur, "ended-only history must report no active session")
}

func TestClient_SessionErrorGuidance(t *testing.T) {
	cases := []struct {
		method, path string
		call         func(c *Client) error
		status       int
		code         string
		want         string
	}{
		{
			call: func(c *Client) error {
				_, _, err := c.ActivateSession(context.Background(), "a", "d")
				return err
			},
			status: 404, code: "device_not_registered", want: "设备未注册",
		},
		{
			call: func(c *Client) error {
				_, _, err := c.ActivateSession(context.Background(), "a", "d")
				return err
			},
			status: 409, code: "device_offline", want: "设备离线",
		},
		{
			call: func(c *Client) error {
				_, err := c.RegisterViewer(context.Background(), "S1", "V1", "")
				return err
			},
			status: 409, code: "session_ended", want: "抓包会话已结束",
		},
		{
			call: func(c *Client) error {
				_, err := c.GetSession(context.Background(), "Sgone")
				return err
			},
			status: 404, code: "session_not_found", want: "抓包会话不存在",
		},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			f := &fakeServer{handler: func(w http.ResponseWriter, r *http.Request) {
				writeErr(w, tc.status, tc.code, tc.code)
			}}
			f.ts = httptest.NewServer(f)
			defer f.ts.Close()
			c := newTestClient(t, f)

			err := tc.call(c)
			require.Error(t, err)
			var ae *APIError
			require.True(t, errors.As(err, &ae))
			assert.Equal(t, tc.status, ae.Status)
			assert.Equal(t, tc.code, ae.Code)
			assert.Contains(t, ae.Message, tc.want)
		})
	}
}
