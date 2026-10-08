package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 回归：19 位整数 ID 精度必须贯穿 规则创建 → Web 列表 → 编辑保存 → 重新加载
// → SDK 增量下发（Mock 响应线）全链路逐位不变。
//
// 复现证据：/chat/sessions_v2 原始响应含会话 ID 1473156146373935104 与
// 1576844185645694976（> JS Number.MAX_SAFE_INTEGER ≈ 9.007e15）。前端曾用
// JSON.parse→JSON.stringify 美化编辑预填，把这两个 ID 舍入为 …5000 后保存，
// 客户端按完整 Int64 ID 匹配导致故事/网聊列表缺项。
//
// 本测试以含上述真实 ID 的响应为输入，只把某会话 unread 改为 3，断言服务端
// 在保存后重新加载（Web 全量列表）与实际下发（SDK 增量拉取）两条出口中，
// unread 变为 3 且其余大整数 ID 与输入逐位一致。服务端 body 全程为字符串
// 直存直发（MockRule.Body string），精度不应在 Go 层丢失。
func TestMockRuleAPI_BigIntIDPrecisionPreserved(t *testing.T) {
	const exactBody = `{"ret":1,"data":{"sessions":[{"session_id":1473156146373935104,"bind_session_id":1576844185645694976,"unread":0}],"active_session_list":[1576844185645694976],"wangliao_active_list":[1576844185645694976]}}`
	const editedBody = `{"ret":1,"data":{"sessions":[{"session_id":1473156146373935104,"bind_session_id":1576844185645694976,"unread":3}],"active_session_list":[1576844185645694976],"wangliao_active_list":[1576844185645694976]}}`

	for _, id := range []string{"1473156146373935104", "1576844185645694976"} {
		if !strings.Contains(exactBody, id) {
			t.Fatalf("test input must contain exact ID %s", id)
		}
	}
	require.Equal(t, strings.Replace(exactBody, `"unread":0`, `"unread":3`, 1), editedBody, "edited body must differ only in unread")

	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/mock-rules"

	// 1) 创建规则（等价于 Web「Mock 此请求」，body 为含大整数 ID 的原始文本）。
	create := map[string]any{
		"method":  "POST",
		"path":    "/chat/sessions_v2",
		"enabled": false,
		"response": map[string]any{
			"statusCode": 200,
			"body":       exactBody,
		},
		//  Step1: this create models a capture-originated rule — D6
		// exempts source-bearing creates from the hand-authored note rule.
		"source": map[string]any{"method": "POST", "path": "/chat/sessions_v2"},
	}
	var created capture.MockRuleView
	resp := doJSON(t, http.MethodPost, base, create, &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NotEmpty(t, created.ID)
	require.Equal(t, exactBody, created.Response.Body, "创建后回包体必须逐位等于输入")

	// 2) 编辑保存：只把 unread 0→3，其余字段原文不动（等价于 Web 编辑表单保存）。
	var updated capture.MockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody(editedBody, "precision regress", boolPtr(true)), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, updated.Enabled)
	assert.True(t, updated.Effective)
	assert.Equal(t, editedBody, updated.Response.Body, "编辑后回包体必须逐位等于期望（unread=3 且大整数 ID 不变）")

	// 3) 规则保存后重新加载：Web 全量列表返回同一字节。
	var list MockRuleListResponse
	doJSON(t, http.MethodGet, base, nil, &list)
	require.Len(t, list.Rules, 1)
	assert.Equal(t, editedBody, list.Rules[0].Response.Body, "重新加载后 Web 列表回包体必须逐位一致")
	// 列表 JSON 中不允许出现科学计数法/舍入形态。
	raw, err := json.Marshal(list)
	require.NoError(t, err)
	for _, bad := range []string{"e+", "1473156146373935000", "1576844185645695000"} {
		assert.False(t, strings.Contains(string(raw), bad), "Web 列表 JSON 不应包含舍入形态 %q", bad)
	}

	// 4) 实际 Mock 下发线：SDK 增量拉取（sinceVersion=0）拿到同一 body 字节，
	//    设备端以此文本直发（编码器为业务方注入，超出本仓库范围）。
	var pull MockRuleListResponse
	doJSON(t, http.MethodGet, base+"?sinceVersion=0", nil, &pull)
	require.Len(t, pull.Rules, 1)
	assert.Equal(t, editedBody, pull.Rules[0].Response.Body, "SDK 下发回包体必须逐位一致")
}
