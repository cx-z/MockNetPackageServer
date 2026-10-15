package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushRuleBody builds a create payload (PushMockRuleInput). Capture-originated
// creates ("一键建规则") carry a source snapshot and may omit the note。
func pushRuleBody(channelID, bizType string, enabled bool) map[string]any {
	return map[string]any{
		"channelId": channelID,
		"bizType":   bizType,
		"enabled":   enabled,
		"match": map[string]any{
			"keyword": "tip",
		},
		"replacePayload": `{"type":"chat","data":{"tip":"mocked"}}`,
		"source": map[string]any{
			"channelId": channelID, "bizType": bizType, "msgId": "77",
			"payloadText": `{"type":"chat","data":{"tip":"real"}}`,
		},
	}
}

// pushUpdateBody builds an edit payload (UpdatePushMockRuleInput). No channelId/
// bizType: the match key is immutable on edit. Content changes require note.
func pushUpdateBody(keyword, replacePayload, note string, enabled *bool) map[string]any {
	m := map[string]any{
		"match":          map[string]any{"keyword": keyword},
		"replacePayload": replacePayload,
		"note":           note,
	}
	if enabled != nil {
		m["enabled"] = *enabled
	}
	return m
}

func pushBoolPtr(b bool) *bool { return &b }

func TestPushMockRuleAPI_CRUDAndVersion(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	// Create (default disabled → 请求体 enabled:false)。
	var created capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", false), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NotEmpty(t, created.ID)
	assert.False(t, created.Enabled)
	assert.False(t, created.Effective)
	assert.Equal(t, "xcpush_tcp", created.ChannelId)

	// Web full list: version=1, rule present, no conflicts.
	var list PushMockRuleListResponse
	resp = doJSON(t, http.MethodGet, base, nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, list.Version)
	assert.Len(t, list.Rules, 1)
	assert.Empty(t, list.Conflicts)
	assert.Equal(t, "xcpush_tcp", list.Rules[0].ChannelId)
	assert.Equal(t, int64(0), list.Rules[0].HitCount)

	// SDK 增量拉取（sinceVersion=0）：只返回 effective 规则；无 sinceVersion=1 变化。
	var sdk sdkPushRuleListResponse
	resp = doJSON(t, http.MethodGet, base+"?sinceVersion=0", nil, &sdk)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, sdk.Version)
	assert.Len(t, sdk.Rules, 0, "disabled rule must not be pushed to the SDK")

	// 更新：内容编辑 + 启停（全量语义回传内容 + note）。
	var updated capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"mocked2"}}`, "测试规则", pushBoolPtr(true)), &updated)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, updated.Enabled)
	assert.True(t, updated.Effective)

	// SDK 增量拉取（sinceVersion=1）：只返 effective 规则，且不携带 Web-only 字段。
	var sdk2 sdkPushRuleListResponse
	resp = doJSON(t, http.MethodGet, base+"?sinceVersion=1", nil, &sdk2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, sdk2.Version)
	require.Len(t, sdk2.Rules, 1)
	raw, err := json.Marshal(sdk2.Rules[0])
	require.NoError(t, err)
	for _, leaked := range []string{`"owner"`, `"updatedBy"`, `"hitCount"`, `"lastHitAt"`, `"source"`, `"note"`, `"conflicts"`} {
		assert.NotContains(t, string(raw), leaked, "SDK pull must not carry Web-only field "+leaked)
	}
	assert.Equal(t, "xcpush_tcp", sdk2.Rules[0].ChannelId)

	// 同版本增量拉取：无变化（空数组）。
	var sdk3 sdkPushRuleListResponse
	resp = doJSON(t, http.MethodGet, base+"?sinceVersion=2", nil, &sdk3)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, sdk3.Version)
	assert.Len(t, sdk3.Rules, 0)

	// 删除 → 204；再次全量列表为空。
	resp = doJSON(t, http.MethodDelete, base+"/"+created.ID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	var list2 PushMockRuleListResponse
	resp = doJSON(t, http.MethodGet, base, nil, &list2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, list2.Rules, 0)
	assert.Equal(t, 3, list2.Version)
}

func TestPushMockRuleAPI_DomainConflict(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	var c1 capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &c1)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// 同域（channelId+bizType）再开一条 enabled → 409 rule_conflict。
	resp = doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	// 不同 bizType 域可开。
	var c2 capture.PushMockRuleView
	resp = doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "2", true), &c2)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// 停用规则（纯 toggle 回传内容、note 空）合法。
	var upd capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+c2.ID, pushUpdateBody("tip", `{"type":"chat","data":{"tip":"mocked"}}`, "", pushBoolPtr(false)), &upd)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, upd.Enabled)
}

func TestPushMockRuleAPI_Validation(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	// 缺 channelId → 400 missing_field。
	body := pushRuleBody("xcpush_tcp", "1", false)
	delete(body, "channelId")
	resp := doJSON(t, http.MethodPost, base, body, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// jsonPath 空但 jsonPathValue 非空 → 400 invalid_field。
	body = pushRuleBody("xcpush_tcp", "1", false)
	body["match"] = map[string]any{"jsonPathValue": "x"}
	resp = doJSON(t, http.MethodPost, base, body, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// jsonPathOp 非法 → 400 invalid_field。
	body = pushRuleBody("xcpush_tcp", "1", false)
	body["match"] = map[string]any{"jsonPath": "$.data.tip", "jsonPathOp": "startswith", "jsonPathValue": "x"}
	resp = doJSON(t, http.MethodPost, base, body, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 合法 JSONPath 条件可创建。
	var created capture.PushMockRuleView
	body = pushRuleBody("xcpush_tcp", "1", false)
	body["match"] = map[string]any{"jsonPath": "$.data.tip", "jsonPathOp": "contains", "jsonPathValue": "x"}
	resp = doJSON(t, http.MethodPost, base, body, &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "$.data.tip", created.Match.JSONPath)
}

func TestPushMockRuleAPI_SDKOpenNoAuth(t *testing.T) {
	// SDK 增量拉取（?sinceVersion）无鉴权也可访问（对齐 HTTP mock-rules 分支）。
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	var sdk sdkPushRuleListResponse
	resp := doJSON(t, http.MethodGet, base+"?sinceVersion=0", nil, &sdk)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, sdk.Version)
}

func TestPushMockRuleAPI_SendCommand(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	// 创建并启用规则。
	var created capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.True(t, created.Effective)

	// POST send → 返回指令视图（快照规则 payload）。
	var cmd capture.PushRuleCommandView
	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, &cmd)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, cmd.ID)
	assert.Equal(t, created.ID, cmd.RuleID)
	assert.Equal(t, "xcpush_tcp", cmd.ChannelId)
	assert.Equal(t, "1", cmd.BizType)
	assert.Equal(t, `{"type":"chat","data":{"tip":"mocked"}}`, cmd.Payload)

	// 心跳响应携带该 pending 指令。
	var hb HeartbeatResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, hb.PushCommands, 1)
	assert.Equal(t, cmd.ID, hb.PushCommands[0].ID)
	assert.Equal(t, cmd.Payload, hb.PushCommands[0].Payload)

	// SDK ack 消费 → 后续心跳不再携带（指令空时字段省略；用新变量避免旧值残留）。
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/push-rule-commands/ack",
		map[string]any{"ids": []string{cmd.ID}}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	var hb2 HeartbeatResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/heartbeat", nil, &hb2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(t, hb2.PushCommands)
}

func TestPushMockRuleAPI_SendMultipleAppends(t *testing.T) {
	// 同一规则连续触发两次 → 两条独立指令（连续模拟推送多条）。
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	var created capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var cmd1, cmd2 capture.PushRuleCommandView
	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, &cmd1)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, &cmd2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEqual(t, cmd1.ID, cmd2.ID, "每次触发必须独立指令")

	var hb HeartbeatResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/devices/com.example.integrating/dev-1/heartbeat", nil, &hb)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, hb.PushCommands, 2)
}

func TestPushMockRuleAPI_SendDisabledAndUnknown(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	// 停用规则 send → 409 push_rule_disabled。
	var created capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", false), &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	// 未知规则 send → 404。
	resp = doJSON(t, http.MethodPost, base+"/no-such-rule/send", nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
