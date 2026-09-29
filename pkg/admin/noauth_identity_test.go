package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/require"
)

// M4: --no-auth 语义修正。--no-auth（apiKeyConfig disabled）不强制登录，
// 但请求带有效 Bearer token 时必须解析身份并注入 UserCtx，使创建规则
// owner 正确落对应用户；缺失/无效 token 仍按现状放行（caller=nil）。

// TestNoAuthValidTokenInjectsOwner：--no-auth 下带有效 token 创建规则，
// owner/updatedBy 必须写入对应用户（修复前恒为空 → 卡片显示「—」且不可编辑）。
func TestNoAuthValidTokenInjectsOwner(t *testing.T) {
	ts := newAuthTestAPI(t) // WithAPIKeyDisabled = --no-auth

	userA := loginToken(t, ts, "noauthA", "secret123")

	// 注册设备（--no-auth 下无 token 也可，这里带上 token 验证带 token 路径）。
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", userA,
		map[string]string{"app": "com.example.integrating", "did": "noauth-dev", "name": "noauth-dev"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	base := ts.URL + "/api/v1/devices/com.example.integrating/noauth-dev/mock-rules"

	// 带 token 创建规则 → 201，owner=noauthA（修复点：之前 caller 恒 nil → owner 空）。
	var created capture.MockRuleView
	res, body := doAuthJSON(t, http.MethodPost, base, userA, ruleBody("POST", "/api/noauth", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &created))
	require.Equal(t, "noauthA", created.Owner)
	require.Equal(t, "noauthA", created.UpdatedBy)
}

// TestNoAuthNoTokenWriteStillPasses：--no-auth 下无 token 的写操作仍放行
// （保留既有 TestRequireAuthNoAuthModeBypasses 语义），创建规则 owner 为空
// （等价存量规则，仅 admin 可管理）。
func TestNoAuthNoTokenWriteStillPasses(t *testing.T) {
	ts := newAuthTestAPI(t) // --no-auth

	// 无 token 注册设备 → 201（放行）。
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", "",
		map[string]string{"app": "com.example.integrating", "did": "noauth-anon", "name": "anon"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	// 无 token 创建规则 → 201，owner 空。
	base := ts.URL + "/api/v1/devices/com.example.integrating/noauth-anon/mock-rules"
	var created capture.MockRuleView
	res, body := doAuthJSON(t, http.MethodPost, base, "", ruleBody("POST", "/api/anon", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &created))
	require.Empty(t, created.Owner)
	require.Empty(t, created.UpdatedBy)
}

// TestNoAuthSDKRulePullNoTokenOpen：--no-auth 下无 token 的 SDK 增量拉规则
// （?sinceVersion=N）仍放行 200（SDK 通道 caller=nil 不受影响）。
func TestNoAuthSDKRulePullNoTokenOpen(t *testing.T) {
	ts := newAuthTestAPI(t) // --no-auth

	// 注册设备（无 token）。
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", "",
		map[string]string{"app": "com.example.integrating", "did": "noauth-sdk", "name": "sdk"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	// SDK 增量拉取：无 token → 200（契约 listMockRules，sinceVersion 分支）。
	res, body := doAuthJSON(t, http.MethodGet,
		ts.URL+"/api/v1/devices/com.example.integrating/noauth-sdk/mock-rules?sinceVersion=0", "", nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var sdk sdkRuleListResponse
	require.NoError(t, json.Unmarshal(body, &sdk))
	require.Equal(t, 0, sdk.Version)
}

// TestAuthWebRuleListStillRequiresToken：第三道门在 auth 模式仍强制——Web 全量
// 列表（无 sinceVersion）无 token → 401；带 token → 200。
func TestAuthWebRuleListStillRequiresToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t) // auth 模式

	tok := loginToken(t, ts, "authlistA", "secret123")
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok,
		map[string]string{"app": "com.example.integrating", "did": "auth-list-dev", "name": "list"})
	require.Equal(t, http.StatusCreated, res.StatusCode)

	base := ts.URL + "/api/v1/devices/com.example.integrating/auth-list-dev/mock-rules"

	// 无 token 的 Web 全量列表 → 401（第三道门 auth 模式强制不变）。
	res, _ = doAuthJSON(t, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	// 带 token → 200。
	res, _ = doAuthJSON(t, http.MethodGet, base, tok, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)

	// 无 token 的 SDK 分支在 auth 模式同样开放（契约不变，与 Web 分支区分）。
	res, _ = doAuthJSON(t, http.MethodGet, base+"?sinceVersion=0", "", nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
}
