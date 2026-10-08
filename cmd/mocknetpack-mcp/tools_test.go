package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpFake is a minimal mocknetpack API server for tool handler tests.
type mcpFake struct {
	ts   *httptest.Server
	body string
	path string
}

func (f *mcpFake) api(mux *http.ServeMux) {
	f.ts = httptest.NewServer(mux)
}

func j(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func setupFake(t *testing.T, mux *http.ServeMux) (*mcpFake, string) {
	t.Helper()
	f := &mcpFake{}
	f.api(mux)
	t.Cleanup(f.ts.Close)
	return f, f.ts.URL
}

func callTool(t *testing.T, srv *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := srv.ListTools()[name]
	require.True(t, ok, "tool %q not registered", name)
	ctx := context.WithValue(context.Background(), ctxBearerKey, "test-key")
	res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	require.NoError(t, err)
	return res
}

// resultText joins the text content of a tool result.
func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// TestMCPServer_RegistersTwelveTools asserts M1 (M12.3): the 12 documented
// tools are registered, each with a description and a JSON schema.
func TestMCPServer_RegistersTwelveTools(t *testing.T) {
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, "http://127.0.0.1:1")

	tools := srv.ListTools()
	want := []string{
		"list_devices",
		"get_device_traffic",
		"get_traffic",
		"create_mock_rule_from_traffic",
		"create_mock_rule",
		"create_share",
		"get_share",
		"set_mock_rule_enabled",
		"update_mock_rule",
		"list_mock_rules",
		"get_mock_rule",
		"delete_mock_rule",
	}
	require.Len(t, tools, len(want))
	for _, name := range want {
		tool, ok := tools[name]
		require.True(t, ok, "missing tool %q", name)
		assert.NotEmpty(t, tool.Tool.Description, "tool %q must carry a description", name)
		assert.NotEmpty(t, tool.Tool.InputSchema, "tool %q must carry an input schema", name)
	}
}

// TestMCPServer_ListDevicesTool asserts the list_devices handler end to end.
func TestMCPServer_ListDevicesTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{
			"devices": []capture.DeviceView{{Device: &capture.Device{App: "com.a", Did: "d1", Name: "n"}, Status: capture.DeviceStatusCapturing}},
			"total":   1,
		})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "list_devices", nil)
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), "com.a")
	assert.Contains(t, resultText(res), "d1")
}

// TestMCPServer_GetDeviceTrafficTool asserts the two-step orchestration tool.
func TestMCPServer_GetDeviceTrafficTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	mux.HandleFunc("/api/v1/sessions/s1/traffic", func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "projection=compact")
		assert.Contains(t, r.URL.RawQuery, "scheme=https")
		writeResp(w, 200, map[string]any{
			"entries": []capture.TrafficEntry{{ID: "t1", Method: "GET", URL: "https://a.com/x", Seq: 7, Mocked: true}},
			"total":   1,
		})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "get_device_traffic", map[string]any{
		"app": "com.a", "did": "d1", "scheme": "https", "method": "GET", "since": float64(6),
	})
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), `"seq": 7`)
	assert.Contains(t, resultText(res), `"mocked": true`)
}

// TestMCPServer_CreateRuleFromTrafficTool asserts the two-step create tool and
// its "default disabled" hint.
func TestMCPServer_CreateRuleFromTrafficTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/traffic/t1", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, capture.TrafficEntry{ID: "t1", Method: "GET", Path: "/api/feed", URL: "https://a.com/api/feed", StatusCode: 200, ResponseBody: `{"ok":1}`, Timestamp: time.Now()})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		var in capture.MockRuleInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		assert.Equal(t, "GET", in.Method)
		assert.Equal(t, "/api/feed", in.Path)
		assert.NotNil(t, in.Source)
		assert.Equal(t, "note-1", in.Note)
		writeResp(w, 201, capture.MockRuleView{MockRule: &capture.MockRule{ID: "r1", Method: "GET", Path: "/api/feed", Enabled: false}, Effective: false})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "create_mock_rule_from_traffic", map[string]any{
		"app": "com.a", "did": "d1", "trafficId": "t1", "note": "note-1",
	})
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), "默认停用")
	assert.Contains(t, resultText(res), "r1")
}

// TestMCPServer_CreateMockRuleFromTraffic_CarriesDecodedSource asserts M12.4/5:
// creating a rule from binary (xcp) traffic via the MCP tool must freeze the
// app-decoded request/response JSON into the rule source — exactly like the Web
// "Mock 此请求" path — so the rule detail view can expand them.
func TestMCPServer_CreateMockRuleFromTraffic_CarriesDecodedSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/traffic/t-xcp", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, capture.TrafficEntry{
			ID: "t-xcp", Method: "POST", Path: "/character/get_display_info_v2",
			URL:            "https://gw.example.com/character/get_display_info_v2",
			StatusCode:     200,
			RequestHeaders: map[string][]string{"Content-Type": {"application/xcp"}},
			RequestBody:    "[binary 480 bytes]", RequestBodyBase64: "AAEC",
			RequestBodyDecoded: `{"character_id":1120180419,"h_app":"dokimo"}`,
			ResponseHeaders:    map[string][]string{"Content-Type": {"application/xcp"}},
			ResponseBody:       "[binary 3808 bytes]", ResponseBodyBase64: "AwQF",
			ResponseBodyDecoded: `{"ret":1,"errcode":1,"data":{"id":1120180419}}`,
			Timestamp:           time.Now(),
		})
	})
	var in capture.MockRuleInput
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeResp(w, 201, capture.MockRuleView{MockRule: &capture.MockRule{ID: "r-xcp", Method: "POST",
			Path: "/character/get_display_info_v2", Response: in.Response, Enabled: false}, Effective: false})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "create_mock_rule_from_traffic", map[string]any{
		"app": "com.a", "did": "d1", "trafficId": "t-xcp", "note": "m12.5 decoded",
	})
	assert.False(t, res.IsError, resultText(res))
	require.NotNil(t, in.Source)
	assert.Equal(t, `{"character_id":1120180419,"h_app":"dokimo"}`, in.Source.RequestBodyDecoded)
	assert.Equal(t, `{"ret":1,"errcode":1,"data":{"id":1120180419}}`, in.Source.ResponseBodyDecoded)
	assert.Equal(t, "AAEC", in.Source.RequestBodyBase64)
	assert.Equal(t, "AwQF", in.Source.ResponseBodyBase64)
	assert.Equal(t, "application/xcp", in.Response.Headers["Content-Type"])
	assert.Contains(t, resultText(res), "默认停用")
}

// TestMCPServer_CreateShareTool asserts create_share returns the share ID and
// expiry.
func TestMCPServer_CreateShareTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/shares", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 201, mnpapi.ShareCreated{ShareID: "share-1", URL: "/mocknetpack/#/share/share-1", ExpiresAt: time.Now().Add(7 * 24 * time.Hour)})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "create_share", map[string]any{"trafficId": "t1"})
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), "share-1")
}

// TestMCPServer_ErrorTransmission asserts M3: 401/404/409 surface as tool
// errors with the AI-facing guidance.
func TestMCPServer_ErrorTransmission(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 401, map[string]string{"error": "unauthorized", "message": "missing token"})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 409, map[string]string{"error": "rule_conflict", "message": "同接口已存在生效规则"})
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}}, "total": 1})
	})
	mux.HandleFunc("/api/v1/sessions/s1/traffic", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 404, map[string]string{"error": "not_found", "message": "会话不存在"})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	// 401 → guidance to configure an API key.
	res := callTool(t, srv, "list_devices", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "MOCKNETPACK_API_KEY")

	// 409 → mutual-exclusion guidance.
	res = callTool(t, srv, "create_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "method": "GET", "path": "/x", "note": "n",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "互斥冲突")

	// 404 → not-found guidance.
	res = callTool(t, srv, "get_device_traffic", map[string]any{"app": "com.a", "did": "d1"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "先查询确认 id 有效")
}

// TestMCPServer_SetMockRuleEnabled_CarriesFullResponse asserts the read-modify-
// write contract for set_mock_rule_enabled: the server PUT is a full replace of
// the canned response (statusCode 100–599 validated), so a pure toggle must
// re-send the complete response and note. Also covers a binary canned body
// (bodyBase64), which must survive the round-trip untouched.
func TestMCPServer_SetMockRuleEnabled_CarriesFullResponse(t *testing.T) {
	ruleJSON := `{
		"id":"r1","app":"com.a","did":"d1","method":"GET","path":"/api/feed",
		"response":{"statusCode":200,"headers":{"Content-Type":"application/xcp"},
		            "body":"(binary)","bodyBase64":"AAECAwQ="},
		"enabled":false,"note":"keep-me","effective":false
	}`
	var gotBodies []capture.UpdateMockRuleInput
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeResp(w, 200, map[string]any{"version": 3, "rules": []json.RawMessage{json.RawMessage(ruleJSON)}})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var in capture.UpdateMockRuleInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		gotBodies = append(gotBodies, in)
		writeResp(w, 200, capture.MockRuleView{
			MockRule: &capture.MockRule{ID: "r1", Method: "GET", Path: "/api/feed",
				Response: in.Response, Note: in.Note, Enabled: in.Enabled != nil && *in.Enabled},
			Effective: in.Enabled != nil && *in.Enabled,
		})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	// enable=true
	res := callTool(t, srv, "set_mock_rule_enabled", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "r1", "enabled": true,
	})
	assert.False(t, res.IsError, resultText(res))
	require.Len(t, gotBodies, 1)
	require.NotNil(t, gotBodies[0].Enabled)
	assert.True(t, *gotBodies[0].Enabled)
	assert.Equal(t, 200, gotBodies[0].Response.StatusCode, "PUT must carry the full response")
	assert.Equal(t, "AAECAwQ=", gotBodies[0].Response.BodyBase64, "binary body must survive read-modify-write")
	assert.Equal(t, "keep-me", gotBodies[0].Note, "existing note must be preserved")
	assert.Contains(t, resultText(res), `"enabled": true`)

	// enable=false → a second toggle re-reads the rule and toggles off.
	res = callTool(t, srv, "set_mock_rule_enabled", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "r1", "enabled": false,
	})
	assert.False(t, res.IsError, resultText(res))
	require.Len(t, gotBodies, 2)
	require.NotNil(t, gotBodies[1].Enabled)
	assert.False(t, *gotBodies[1].Enabled)
	assert.Equal(t, 200, gotBodies[1].Response.StatusCode)
	assert.Equal(t, "AAECAwQ=", gotBodies[1].Response.BodyBase64)
}

// TestMCPServer_SetMockRuleEnabled_MissingRule404 asserts the guidance when the
// rule id is unknown: read fails with a 404-style message, no PUT is sent.
func TestMCPServer_SetMockRuleEnabled_MissingRule404(t *testing.T) {
	var putCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeResp(w, 200, map[string]any{"version": 3, "rules": []any{}})
			return
		}
		putCount++
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "set_mock_rule_enabled", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "missing", "enabled": true,
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "请先 list 确认规则 ID")
	assert.Zero(t, putCount, "unknown rule must not produce a PUT")
}

// TestMCPServer_ListMockRulesTool asserts P0-1: list_mock_rules returns the
// slim per-rule view and supports path filtering.
func TestMCPServer_ListMockRulesTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{"version": 3, "rules": []capture.MockRuleView{
			{MockRule: &capture.MockRule{ID: "r1", Method: "GET", Path: "/api/feed", Enabled: true, Note: "feed"}, Effective: true},
			{MockRule: &capture.MockRule{ID: "r2", Method: "POST", Path: "/api/user/login", Enabled: false, Note: "login"}, Effective: false},
		}})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	// full list
	res := callTool(t, srv, "list_mock_rules", map[string]any{"app": "com.a", "did": "d1"})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "feed")
	assert.Contains(t, resultText(res), "login")

	// path filter
	res = callTool(t, srv, "list_mock_rules", map[string]any{"app": "com.a", "did": "d1", "path": "/api/feed"})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "r1")
	assert.NotContains(t, resultText(res), "r2")
}

// TestMCPServer_GetMockRuleTool asserts P0-2: get_mock_rule returns the full
// rule with its response mode label.
func TestMCPServer_GetMockRuleTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{"version": 3, "rules": []capture.MockRuleView{
			{MockRule: &capture.MockRule{ID: "r1", Method: "GET", Path: "/api/feed",
				Response: capture.MockResponse{StatusCode: 200, Body: "(binary)", BodyBase64: "AAEC"},
				Enabled:  true, Note: "keep"}},
		}})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "get_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "r1"})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), `"responseMode": "binary"`)
	assert.Contains(t, resultText(res), "AAEC")
}

// TestMCPServer_UpdateMockRule_ClearBodyBase64 asserts P0-3: clearBodyBase64
// drops the binary snapshot, the PUT carries an empty bodyBase64, and the
// result labels the final mode as text.
func TestMCPServer_UpdateMockRule_ClearBodyBase64(t *testing.T) {
	ruleJSON := `{"id":"r1","method":"GET","path":"/api/feed",
		"response":{"statusCode":200,"body":"(binary)","bodyBase64":"AAECAwQ="},
		"enabled":true,"note":"old-note","effective":true}`
	var got capture.UpdateMockRuleInput
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeResp(w, 200, map[string]any{"version": 3, "rules": []json.RawMessage{json.RawMessage(ruleJSON)}})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules/r1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeResp(w, 200, capture.MockRuleView{MockRule: &capture.MockRule{
			ID: "r1", Method: "GET", Path: "/api/feed",
			Response: got.Response, Note: got.Note, Enabled: true,
		}, Effective: true})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "update_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "r1",
		"body": `{"ok":true}`, "clearBodyBase64": true, "note": "to-text",
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Empty(t, got.Response.BodyBase64, "binary snapshot must be cleared")
	assert.Equal(t, `{"ok":true}`, got.Response.Body)
	assert.Contains(t, resultText(res), `"responseMode": "text"`)

	// without clearBodyBase64 the binary body survives the round-trip
	res = callTool(t, srv, "update_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "r1", "note": "keep-binary",
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Equal(t, "AAECAwQ=", got.Response.BodyBase64)
	assert.Contains(t, resultText(res), `"responseMode": "binary"`)
}

// TestMCPServer_DeleteMockRuleTool asserts P1-2: delete_mock_rule issues DELETE
// and reports success.
func TestMCPServer_DeleteMockRuleTool(t *testing.T) {
	var deleted bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules/r1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.12.0")
	registerTools(srv, base)

	res := callTool(t, srv, "delete_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "r1"})
	assert.False(t, res.IsError, resultText(res))
	assert.True(t, deleted)
	assert.Contains(t, resultText(res), "已删除")
}

func writeResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var _ = strings.TrimSpace
