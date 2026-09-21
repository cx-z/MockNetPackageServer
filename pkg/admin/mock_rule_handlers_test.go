package admin

import (
	"net/http"
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

func TestMockRuleAPI_CRUDAndVersion(t *testing.T) {
	srv := newCaptureTestAPI(t)
	base := srv.URL + "/api/v1/devices/com.example.app/dev-1/mock-rules"

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
	base := srv.URL + "/api/v1/devices/com.example.app/dev-1/mock-rules"

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
	base := srv.URL + "/api/v1/devices/com.example.app/dev-1/mock-rules"

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

func TestMockRuleAPI_HeartbeatCarriesRulesVersion(t *testing.T) {
	srv := newCaptureTestAPI(t)

	// Register the device (heartbeat requires it).
	var reg RegisterDeviceResponse
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/register", map[string]any{
		"app": "com.example.app", "did": "dev-1",
	}, &reg)

	var hb HeartbeatResponse
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/heartbeat", nil, &hb)
	assert.Equal(t, 0, hb.RulesVersion, "no rules yet")

	// Create a rule -> version 1.
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/mock-rules",
		ruleBody("GET", "/api/b", false), nil)

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.app/dev-1/heartbeat", nil, &hb)
	assert.Equal(t, 1, hb.RulesVersion, "heartbeat reports bumped rule version")
}

// --- M5: note validation & immutable match key ------------------------------

func TestMockRuleAPI_EditNoteValidation(t *testing.T) {
	srv := newCaptureTestAPI(t)
	base := srv.URL + "/api/v1/devices/com.example.app/dev-1/mock-rules"

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
	base := srv.URL + "/api/v1/devices/com.example.app/dev-1/mock-rules"

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
