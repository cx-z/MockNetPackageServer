package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/require"
)

// M15 方案 D 阶段二端到端：大图抓包 → 建规则回源存 blob → SDK 按 media 拉取。
// 覆盖路由（含 open 的 media 路由）、版本门控、设备隔离与回源失败 400 原因码。

func registerDeviceWithSDK(t *testing.T, srv *httptest.Server, app, did, sdkVersion string) {
	t.Helper()
	// SDK auto-registration is retired: the device must exist (Web manual create)
	// before /devices/register can refresh its SDKVersion.
	mustSeedDevice(t, srv, app, did)
	body, err := json.Marshal(map[string]string{
		"app": app, "did": did, "sdkVersion": sdkVersion, "deviceName": "m15-test",
	})
	require.NoError(t, err)
	resp, err := http.Post(srv.URL+"/api/v1/devices/register", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "register device")
	resp.Body.Close()
}

func bigImageTraffic(t *testing.T, srv *httptest.Server, app, did, url string) string {
	t.Helper()
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		map[string]string{"app": app, "did": did}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	entry := &capture.TrafficEntry{
		Method: "GET", URL: url, Timestamp: time.Now(), DurationMs: 12, StatusCode: 200,
		RequestHeaders: map[string][]string{"Authorization": {"Bearer tok-123"}},
		ResponseHeaders: map[string][]string{
			"Content-Type":   {"image/jpeg"},
			"Content-Length": {"2000000"},
		},
		ResponseBody:       "[binary 2000000 bytes]",
		ResponseBodyBase64: "iVBORw0KGgoAAAA",
	}
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/traffic", TrafficUploadRequest{
		App: app, Did: did, SessionID: session.ID, Entries: []*capture.TrafficEntry{entry},
	}, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// 上传响应不回传条目 ID：从会话流量列表取服务端生成的 ID。
	var out struct {
		Entries []*capture.TrafficEntry `json:"entries"`
	}
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/traffic", nil, &out)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, out.Entries, 1)
	return out.Entries[0].ID
}

func TestMockRuleMedia_EndToEnd(t *testing.T) {
	imgBytes := []byte("\x89PNG-original-image-bytes")
	var authSeen string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	}))
	defer origin.Close()

	srv := newCaptureTestAPI(t)
	registerDeviceWithSDK(t, srv, "com.example.integrating", "d1", "1.1.1")
	trafficID := bigImageTraffic(t, srv, "com.example.integrating", "d1", origin.URL+"/cover.jpg")

	var rule capture.MockRule
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/d1/mock-rules", capture.MockRuleInput{
		Method: "GET", Path: "/cover.jpg",
		Response:        capture.MockResponse{StatusCode: 200},
		Enabled:         false,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/cover.jpg"},
		OriginTrafficID: trafficID,
	}, &rule)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NotNil(t, rule.Response.Media, "rule should carry a MediaRef")
	require.Equal(t, int64(len(imgBytes)), rule.Response.Media.Size)
	require.Equal(t, "Bearer tok-123", authSeen, "captured Authorization must be forwarded to the origin")

	// media 路由：SDK 通道（无 Bearer）也能取字节，校验状态/头/内容。
	mediaResp, err := http.Get(srv.URL + "/api/v1/devices/com.example.integrating/d1/mock-rules/" + rule.ID + "/media")
	require.NoError(t, err)
	defer mediaResp.Body.Close()
	require.Equal(t, http.StatusOK, mediaResp.StatusCode)
	require.Equal(t, "image/jpeg", mediaResp.Header.Get("Content-Type"))
	require.Equal(t, `"`+rule.Response.Media.SHA256+`"`, mediaResp.Header.Get("ETag"))
	body, err := io.ReadAll(mediaResp.Body)
	require.NoError(t, err)
	require.Equal(t, imgBytes, body)

	// 设备隔离：同一 ruleId 换 did → 404（不串设备）。
	wrong := doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices/com.example.integrating/d2/mock-rules/"+rule.ID+"/media", nil, nil)
	require.Equal(t, http.StatusNotFound, wrong.StatusCode)

	// 删除规则后 blob 同步消失 → media 404。
	resp = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/devices/com.example.integrating/d1/mock-rules/"+rule.ID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/devices/com.example.integrating/d1/mock-rules/"+rule.ID+"/media", nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestMockRuleMedia_LegacySDKAndFetchFailure(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gone.jpg") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("img"))
	}))
	defer origin.Close()

	srv := newCaptureTestAPI(t)
	// 1.1.0 低于 MockMediaMinSDKVersion：不回源存 blob，回落 302 → 原 URL
	// （旧 SDK 没有 media 拉取能力，只给占位 body 等于下发不可用规则）。
	registerDeviceWithSDK(t, srv, "com.example.integrating", "d1", "1.1.0")
	trafficID := bigImageTraffic(t, srv, "com.example.integrating", "d1", origin.URL+"/legacy.jpg")
	var rule capture.MockRule
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/d1/mock-rules", capture.MockRuleInput{
		Method: "GET", Path: "/legacy.jpg",
		Response:        capture.MockResponse{StatusCode: 200, Body: "[binary 2000000 bytes]"},
		Enabled:         false,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/legacy.jpg"},
		OriginTrafficID: trafficID,
	}, &rule)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Nil(t, rule.Response.Media, "legacy SDK must not get a media rule")
	require.Equal(t, http.StatusFound, rule.Response.StatusCode, "legacy SDK falls back to 302")
	require.Equal(t, origin.URL+"/legacy.jpg", rule.Response.Headers["Location"])

	// 1.1.1 + 源站 404：创建返回 400 media_unfetchable + http_4xx，规则不落库。
	registerDeviceWithSDK(t, srv, "com.example.integrating", "d3", "1.1.1")
	trafficID = bigImageTraffic(t, srv, "com.example.integrating", "d3", origin.URL+"/gone.jpg")
	var errBody struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/d3/mock-rules", capture.MockRuleInput{
		Method: "GET", Path: "/gone.jpg",
		Response:        capture.MockResponse{StatusCode: 200},
		Enabled:         false,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/gone.jpg"},
		OriginTrafficID: trafficID,
	}, &errBody)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "media_unfetchable", errBody.Error)
	require.Equal(t, "http_4xx", errBody.Code)
	require.NotEmpty(t, errBody.Message, fmt.Sprintf("message empty: %+v", errBody))
}
