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
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, ruleBody("POST", "/api/a", true), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, updated.Enabled)
	assert.True(t, updated.Effective)

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
	resp = doJSON(t, http.MethodPut, base+"/"+r2.ID, ruleBody("POST", "/api/a", true), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// Disable r1 first, then enable r2 succeeds.
	resp = doJSON(t, http.MethodPut, base+"/"+r1.ID, ruleBody("POST", "/api/a", false), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doJSON(t, http.MethodPut, base+"/"+r2.ID, ruleBody("POST", "/api/a", true), nil)
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
	resp = doJSON(t, http.MethodPut, base+"/no-such", ruleBody("GET", "/a", true), nil)
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
