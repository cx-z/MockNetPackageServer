package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ruleBody(method, path string, enabled bool) map[string]any {
	return map[string]any{
		"method":  method,
		"path":    path,
		"enabled": enabled,
		"response": map[string]any{
			"statusCode": 200,
			"body":       `{"ok":true}`,
		},
		// M11 Step1: existing HTTP tests model capture-originated rules
		// ("Mock 此请求") — they carry a source snapshot and may omit the
		// note (D6 exempts source-bearing creates).
		"source": map[string]any{"method": method, "path": path},
	}
}

// updateBody builds an edit payload (M5 UpdateMockRuleInput). No method/path:
// the match key is immutable on edit. note is required when the canned response
// actually changes (editing); a pure toggle (response echoed unchanged) may
// leave it blank (M7). enabled may be omitted (nil) to leave the switch
// untouched.
func updateBody(body, note string, enabled *bool) map[string]any {
	m := map[string]any{
		"note": note,
		"response": map[string]any{
			"statusCode": 200,
			"body":       body,
		},
	}
	if enabled != nil {
		m["enabled"] = *enabled
	}
	return m
}

// seedMockRuleDevice registers the default com.example.integrating/dev-1 device so the
// mock-rule ownership check (M7.2.2) has a device to authorize.
func seedMockRuleDevice(t *testing.T, srv *httptest.Server) {
	t.Helper()
	mustSeedDevice(t, srv, "com.example.integrating", "dev-1")
}

func TestMockRuleAPI_CRUDAndVersion(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// Create (default disabled).
	var created capture.MockRuleView
	resp := doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", false), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NotEmpty(t, created.ID)
	assert.False(t, created.Enabled)
	assert.False(t, created.Effective)

	// Web full list: version=1, rule present, no conflicts.
	var list MockRuleListResponse
	resp = doJSON(t, http.MethodGet, base, nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, list.Version)
	assert.Len(t, list.Rules, 1)
	assert.Empty(t, list.Conflicts)

	// Enable it via PUT -> version bumps to 2, effective now true.
	var updated capture.MockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(`{"ok":true}`, "first note", boolPtr(true)), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, updated.Enabled)
	assert.True(t, updated.Effective)
	assert.Equal(t, "first note", updated.Note)

	// SDK pull at version 2: unchanged -> empty rules.
	var pull MockRuleListResponse
	doJSON(t, http.MethodGet, base+"?sinceVersion=2", nil, &pull)
	assert.Equal(t, 2, pull.Version)
	assert.Empty(t, pull.Rules)
	assert.Empty(t, pull.Conflicts)

	// SDK pull at version 0: gets the single effective rule.
	doJSON(t, http.MethodGet, base+"?sinceVersion=0", nil, &pull)
	assert.Equal(t, 2, pull.Version)
	assert.Len(t, pull.Rules, 1)
	assert.Equal(t, "/api/a", pull.Rules[0].Path)
}

func TestMockRuleAPI_SameInterfaceConflict(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// First enabled rule.
	var r1 capture.MockRuleView
	resp := doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", true), &r1)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "first create")

	// Second enabled on the same interface -> 409.
	resp = doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", true), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// A disabled second rule is allowed.
	var r2 capture.MockRuleView
	resp = doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", false), &r2)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Enabling r2 against r1 -> 409.
	resp = doJSON(t, http.MethodPut, base+"/"+r2.ID, updateBody(`{"ok":true}`, "enable r2", boolPtr(true)), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// Disable r1 first, then enable r2 succeeds.
	resp = doJSON(t, http.MethodPut, base+"/"+r1.ID, updateBody(`{"ok":true}`, "disable r1", boolPtr(false)), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doJSON(t, http.MethodPut, base+"/"+r2.ID, updateBody(`{"ok":true}`, "enable r2", boolPtr(true)), nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMockRuleAPI_ValidationAndNotFound(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// Missing method.
	resp := doJSON(t, http.MethodPost, base, map[string]any{"path": "/a", "response": map[string]any{"statusCode": 200}}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// Missing statusCode.
	resp = doJSON(t, http.MethodPost, base, map[string]any{"method": "GET", "path": "/a", "response": map[string]any{}}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// sinceVersion not an int.
	resp = doJSON(t, http.MethodGet, base+"?sinceVersion=abc", nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Update unknown rule -> 404.
	resp = doJSON(t, http.MethodPut, base+"/no-such", updateBody(`{"ok":true}`, "note", boolPtr(true)), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMockRuleAPI_InvalidStatusDiagnosis asserts the 400 diagnosis names the
// failing field and phase (M12.2): a zero statusCode on PUT must explain the
// full-replace semantics; an out-of-range value must carry the actual number.
// The message must never echo Authorization/API-key material (only statusCode).
func TestMockRuleAPI_InvalidStatusDiagnosis(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"
	var created capture.MockRuleView
	resp2 := doJSON(t, http.MethodPost, base, ruleBody("GET", "/api/diag", false), &created)
	require.Equal(t, http.StatusCreated, resp2.StatusCode)

	// PUT with an empty response (pure toggle sent as {enabled:true}).
	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	resp := doJSON(t, http.MethodPut, base+"/"+created.ID, map[string]any{"enabled": true}, &errResp)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_field", errResp.Error)
	assert.Contains(t, errResp.Message, "response.statusCode")
	assert.Contains(t, errResp.Message, "整体覆盖更新", "zero statusCode on PUT must explain full-replace semantics")

	// POST with an out-of-range value.
	resp = doJSON(t, http.MethodPost, base, map[string]any{
		"method": "GET", "path": "/z", "response": map[string]any{"statusCode": 700},
	}, &errResp)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, errResp.Message, "700")
	assert.Contains(t, errResp.Message, "100–599")

	// PUT with an out-of-range value.
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, map[string]any{
		"response": map[string]any{"statusCode": 99},
	}, &errResp)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, errResp.Message, "99")
}

func TestMockRuleAPI_HeartbeatCarriesRulesVersion(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)

	// Register the device (heartbeat requires it).
	var reg RegisterDeviceResponse
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register", map[string]any{
		"app": "com.example.integrating", "did": "dev-1",
	}, &reg)

	var hb HeartbeatResponse
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/heartbeat", nil, &hb)
	assert.Equal(t, 0, hb.RulesVersion, "no rules yet")

	// Create a rule -> version 1.
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/mock-rules",
		ruleBody("GET", "/api/b", false), nil)

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/heartbeat", nil, &hb)
	assert.Equal(t, 1, hb.RulesVersion, "heartbeat reports bumped rule version")
}

// --- M5: note validation & immutable match key ------------------------------

func TestMockRuleAPI_EditNoteValidation(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	var created capture.MockRuleView
	doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", false), &created)

	// Pure toggle (response unchanged) with blank note -> 200 (M7): rules
	// created from a capture carry no note and must be enableable directly.
	var toggled capture.MockRuleView
	resp := doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(`{"ok":true}`, "", boolPtr(true)), &toggled)
	require.Equal(t, http.StatusOK, resp.StatusCode, "pure toggle with blank note allowed")
	assert.True(t, toggled.Enabled)

	// Editing the response with an empty note -> 400.
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(`{"changed":true}`, "", boolPtr(false)), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "empty note rejected on edit")
	// Whitespace-only note -> 400.
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(`{"changed":true}`, "   ", boolPtr(false)), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "whitespace note rejected on edit")

	// Valid note -> 200 and round-trips.
	var updated capture.MockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(`{"changed":true}`, "verify feed anomaly", boolPtr(false)), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "verify feed anomaly", updated.Note)
	assert.Equal(t, `{"changed":true}`, updated.Response.Body)
	// Match key untouched even though the edit body carried no method/path.
	assert.Equal(t, "POST", updated.Method)
	assert.Equal(t, "/api/a", updated.Path)
}

func TestMockRuleAPI_EditOmitsEnabledKeepsSwitch(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// Start disabled.
	var created capture.MockRuleView
	doJSON(t, http.MethodPost, base, ruleBody("POST", "/api/a", false), &created)
	require.False(t, created.Enabled)

	// Edit body omits "enabled" entirely -> switch must stay off.
	var updated capture.MockRuleView
	resp := doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody("v2", "memo", nil), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, updated.Enabled, "enabled must not flip when omitted from edit body")
	assert.False(t, updated.Effective)
}

// O4.1 序列化出口：Web 全量列表下发 owner/updatedBy；SDK 增量拉取（同一路由
// ?sinceVersion 分支）绝不携带 owner/updatedBy 及 source/note/lastUsedAt 等
// 纯服务端/Web-only 字段（契约 v0.11.0：SDK 只关心 method/path/response/enabled/effective）。
func TestMockRuleAPI_OwnerFields_WebVsSDK(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	devA := freshDevToken(t, ts, "ownerA")
	base := ts.URL + "/api/v1/devices/com.example.integrating/rule-owner-dev/mock-rules"

	// 设备由 devA 注册（M7.2.2 数据隔离：devA 可见自己的设备）。
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", devA,
		map[string]string{"app": "com.example.integrating", "did": "rule-owner-dev", "name": "A"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	// devA 创建规则 → owner 写入当前会话用户名。
	var created capture.MockRuleView
	res, body := doAuthJSON(t, http.MethodPost, base, devA, ruleBody("POST", "/api/owned", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &created))
	assert.Equal(t, "ownerA", created.Owner)
	assert.Equal(t, "ownerA", created.UpdatedBy)

	// 启用（owner 有权），使规则 effective，SDK 拉取才能拿到它。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+created.ID, devA,
		updateBody(`{"ok":true}`, "enable for sdk", boolPtr(true)))
	require.Equal(t, http.StatusOK, res.StatusCode)

	// Web 全量列表（devA token）：下发 owner/updatedBy。
	res, body = doAuthJSON(t, http.MethodGet, base, devA, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var webList MockRuleListResponse
	require.NoError(t, json.Unmarshal(body, &webList))
	require.Len(t, webList.Rules, 1)
	assert.Equal(t, "ownerA", webList.Rules[0].Owner)
	assert.Equal(t, "ownerA", webList.Rules[0].UpdatedBy)

	// SDK 增量拉取（无需 token；?sinceVersion=0）：序列化结果必须不含纯服务端字段。
	res, body = doAuthJSON(t, http.MethodGet, base+"?sinceVersion=0", "", nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	raw := string(body)
	for _, leaked := range []string{`"owner"`, `"updatedBy"`, `"source"`, `"note"`, `"lastUsedAt"`, `"conflicts"`} {
		if strings.Contains(raw, leaked) {
			t.Errorf("SDK pull leaks %s: %s", leaked, raw)
		}
	}
	var sdkList sdkRuleListResponse
	require.NoError(t, json.Unmarshal(body, &sdkList))
	require.Len(t, sdkList.Rules, 1)
	assert.Equal(t, "POST", sdkList.Rules[0].Method)
	assert.Equal(t, "/api/owned", sdkList.Rules[0].Path)
	assert.True(t, sdkList.Rules[0].Effective)
}

// --- M11 Step1: zero-from-scratch (hand-authored) rule create validation ----

func TestMockRuleAPI_HandAuthoredRules(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// Hand-authored body: no "source" key (D6 path), always with a note unless
	// the test deliberately omits it.
	handBody := func(method, path, note string) map[string]any {
		return map[string]any{
			"method":  method,
			"path":    path,
			"enabled": false,
			"note":    note,
			"response": map[string]any{
				"statusCode": 200,
				"body":       `{"hello":"world"}`,
			},
		}
	}

	// S2': path not starting with '/' -> 400.
	resp := doJSON(t, http.MethodPost, base, handBody("GET", "api/login", "memo"), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "path without leading '/' must be rejected")
	// S2': path containing '?' (query string pasted in) -> 400.
	resp = doJSON(t, http.MethodPost, base, handBody("GET", "/api/login?x=1", "memo"), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "path with query string must be rejected")
	// S2: hand-authored create with no note key -> 400 (D6).
	nosrc := handBody("GET", "/api/hello", "")
	delete(nosrc, "note")
	resp = doJSON(t, http.MethodPost, base, nosrc, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "no source + blank note must be rejected")
	// Whitespace-only note -> 400 as well.
	resp = doJSON(t, http.MethodPost, base, handBody("GET", "/api/hello", "   "), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "whitespace note must be rejected")

	// S1: valid hand-authored create -> 201, response carries no source
	// snapshot, note round-trips, default disabled (D4).
	var created capture.MockRuleView
	resp = doJSON(t, http.MethodPost, base, handBody("GET", "/api/hello", "mock greeting for UI dev"), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "valid hand-authored create")
	assert.Empty(t, created.Source, "hand-authored rule must have no source snapshot")
	assert.Equal(t, "mock greeting for UI dev", created.Note)
	assert.False(t, created.Enabled)
}
