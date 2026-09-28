package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// O4.2/O4.3 权限矩阵（auth 模式）。设备隔离（M7.2.2）仍生效——这里"他人规则"
// 指同一设备上其他用户创建的规则（规则对所有设备可见者共享，但只有 owner/admin 可改）。

// seedRulePermDevice 注册权限测试设备并返回 base URL。
func seedRulePermDevice(t *testing.T, ts *httptest.Server, ownerToken, app, did string) string {
	t.Helper()
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", ownerToken,
		map[string]string{"app": app, "did": did, "name": "perm-" + did})
	require.Equal(t, http.StatusCreated, res.StatusCode)
	return ts.URL + "/api/v1/devices/" + app + "/" + did + "/mock-rules"
}

func TestRulePermission_Matrix(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	ctx := context.Background()
	devA := freshDevToken(t, ts, "permA")
	require.NoError(t, api.CreateAdminUser(ctx, "root", "secret123"))
	admin := loginToken(t, ts, "root", "secret123")

	base := seedRulePermDevice(t, ts, devA, "com.example.integrating", "perm-dev")

	// devA 创建规则 r1（owner=permA）。
	var r1 capture.MockRuleView
	res, body := doAuthJSON(t, http.MethodPost, base, devA, ruleBody("POST", "/api/a", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &r1))
	assert.Equal(t, "permA", r1.Owner)

	// admin 在 devA 设备上建规则 r2（owner=root）——典型"设备可见但规则不可改"场景。
	var r2 capture.MockRuleView
	res, body = doAuthJSON(t, http.MethodPost, base, admin, ruleBody("GET", "/api/admin-rule", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &r2))
	assert.Equal(t, "root", r2.Owner)

	// 1) owner 编辑自己规则 + toggle → 200。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+r1.ID, devA,
		updateBody(`{"v":2}`, "permA edit", boolPtr(true)))
	require.Equal(t, http.StatusOK, res.StatusCode)

	// 2) 设备 owner dev 编辑 admin 创建的规则（非 owner 非 admin）→ 403。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+r2.ID, devA,
		updateBody(`{"hack":true}`, "permA tries admin rule", boolPtr(true)))
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	// 3) toggle 他人规则（纯开关，PUT enabled）→ 403。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+r2.ID, devA,
		map[string]any{"response": r2.Response, "note": r2.Note, "enabled": false})
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	// 4) 删除他人规则 → 403（规则仍在）。
	res, _ = doAuthJSON(t, http.MethodDelete, base+"/"+r2.ID, devA, nil)
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	// 5) admin 全权：编辑并删除 devA 的规则 r1 → 200 / 204。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+r1.ID, admin,
		updateBody(`{"v":9}`, "admin edit", boolPtr(false)))
	require.Equal(t, http.StatusOK, res.StatusCode)
	res, _ = doAuthJSON(t, http.MethodDelete, base+"/"+r1.ID, admin, nil)
	require.Equal(t, http.StatusNoContent, res.StatusCode)

	// 6) 设备可见者（devA）对不存在 ruleID → 403（不泄露存在性；admin 才见真实 404）。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/no-such-rule", devA,
		updateBody(`{"x":1}`, "probe", nil))
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	res, _ = doAuthJSON(t, http.MethodDelete, base+"/no-such-rule", devA, nil)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	res, _ = doAuthJSON(t, http.MethodPut, base+"/no-such-rule", admin,
		updateBody(`{"x":1}`, "probe", nil))
	require.Equal(t, http.StatusNotFound, res.StatusCode)

	// 7) 查看：列表对设备可见者开放——r1 已删，剩 admin 建的 r2，devA 可见只读。
	res, body = doAuthJSON(t, http.MethodGet, base, devA, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var list MockRuleListResponse
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Rules, 1)
	assert.Equal(t, r2.ID, list.Rules[0].ID)
	assert.Equal(t, "root", list.Rules[0].Owner)
}


func TestRulePermission_LegacyEmptyOwner_AdminOnly(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	ctx := context.Background()
	devA := freshDevToken(t, ts, "legacyA")
	devB := freshDevToken(t, ts, "legacyB")
	require.NoError(t, api.CreateAdminUser(ctx, "root", "secret123"))
	admin := loginToken(t, ts, "root", "secret123")

	base := seedRulePermDevice(t, ts, devA, "com.example.integrating", "legacy-dev")

	// 存量规则：owner 空。auth 模式下只能经 store 直插（handler 创建必带 owner）。
	// 经 CaptureManager 以 nil caller 创建 = --no-auth 形态（owner 空）。
	api.captureManager.CreateMockRule(ctx, "com.example.integrating", "legacy-dev",
		&capture.MockRuleInput{Method: "POST", Path: "/api/legacy",
			Response: capture.MockResponse{StatusCode: 200, Body: `{"legacy":true}`}}, nil)

	// dev（owner 空 → 非 admin）PUT/DELETE → 403。
	var list MockRuleListResponse
	res, body := doAuthJSON(t, http.MethodGet, base, devA, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Rules, 1)
	legacyID := list.Rules[0].ID
	assert.Empty(t, list.Rules[0].Owner)

	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+legacyID, devA,
		updateBody(`{"x":1}`, "legacy edit", nil))
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	// devB 非设备 owner → 设备隔离（M7.2.2）先于规则权限：404 而非 403。
	res, _ = doAuthJSON(t, http.MethodDelete, base+"/"+legacyID, devB, nil)
	require.Equal(t, http.StatusNotFound, res.StatusCode)

	// admin 可管理存量规则 → 200 / 204。
	res, _ = doAuthJSON(t, http.MethodPut, base+"/"+legacyID, admin,
		updateBody(`{"admin":1}`, "admin fixes legacy", boolPtr(true)))
	require.Equal(t, http.StatusOK, res.StatusCode)
	res, _ = doAuthJSON(t, http.MethodDelete, base+"/"+legacyID, admin, nil)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
}

func TestRulePermission_CreateByAnyDev_StampsOwner(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	devA := freshDevToken(t, ts, "createA")
	devB := freshDevToken(t, ts, "createB")

	// 每个开发者在自己可见的设备上均可创建（"Mock 此请求"全开发者可用）。
	// 设备隔离下 devB 不能访问 devA 的设备（404），故 devB 注册自己的设备再创建。
	baseB := seedRulePermDevice(t, ts, devB, "com.example.integrating", "create-dev-b")
	_ = seedRulePermDevice(t, ts, devA, "com.example.integrating", "create-dev-a")

	// devB 访问 devA 的设备 → 404（设备隔离，M7.2.2，与规则权限无关）。
	res, _ := doAuthJSON(t, http.MethodPost,
		ts.URL+"/api/v1/devices/com.example.integrating/create-dev-a/mock-rules",
		devB, ruleBody("PUT", "/api/created", false))
	require.Equal(t, http.StatusNotFound, res.StatusCode)

	// devB 在自己的设备上创建 → 201，owner=createB。
	var created capture.MockRuleView
	res, body := doAuthJSON(t, http.MethodPost, baseB, devB, ruleBody("PUT", "/api/created", false))
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.NoError(t, json.Unmarshal(body, &created))
	assert.Equal(t, "createB", created.Owner)
	assert.Equal(t, "createB", created.UpdatedBy)
}
