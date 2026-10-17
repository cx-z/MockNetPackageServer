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

// pushUpdateBody builds an edit payload (UpdatePushMockRuleInput).
// 2026-10-16（用户拍板放开匹配键编辑）：可选 domain map 携带 channelId/bizType
// 迁移匹配键域（指针语义：缺省 = 不修改）。note 为空时不发送 note 字段（服务端
// 指针语义：未提供 = 保持现值，纯 toggle 不清空既有备注）。
func pushUpdateBody(keyword, replacePayload, note string, enabled *bool, domain ...map[string]any) map[string]any {
	m := map[string]any{
		"match":          map[string]any{"keyword": keyword},
		"replacePayload": replacePayload,
	}
	if note != "" {
		m["note"] = note
	}
	if enabled != nil {
		m["enabled"] = *enabled
	}
	for _, d := range domain {
		for k, v := range d {
			m[k] = v
		}
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

	// 2026-10-16（用户拍板）：push mock 模板池允许多条同域（channelId+bizType）
	// enabled 模板共存（模拟推送按规则 ID 快照，无域内匹配歧义）——同域再开
	// 一条 enabled 不再 409，两条都 Effective=true，无 conflicts 报告。
	var c1 capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &c1)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.True(t, c1.Effective)

	var c2 capture.PushMockRuleView
	resp = doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &c2)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.True(t, c2.Effective, "同域多条 enabled 全部 Effective（模板池语义）")

	var list PushMockRuleListResponse
	resp = doJSON(t, http.MethodGet, base, nil, &list)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, list.Rules, 2)
	for _, r := range list.Rules {
		assert.True(t, r.Effective)
	}
	assert.Empty(t, list.Conflicts, "多条同域 enabled 不再是异常，无 conflicts 报告")

	// 停用规则（纯 toggle 回传内容、note 空）合法。
	var upd capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+c2.ID, pushUpdateBody("tip", `{"type":"chat","data":{"tip":"mocked"}}`, "", pushBoolPtr(false)), &upd)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, upd.Enabled)
}

// TestPushMockRuleAPI_UpdateMigratesDomain：2026-10-16 用户拍板放开匹配键编辑。
// 编辑可迁移 channelId+bizType 域；模板池语义（同轮拍板）：目标域已有活跃
// 规则不再 409（多条同域共存全部 Effective）；channelId 显式置空 → 400。
func TestPushMockRuleAPI_UpdateMigratesDomain(t *testing.T) {
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	// A 在 (xcpush_tcp, "1") 域。
	var a capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &a)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// 编辑 A 迁移到 (xcpush_tcp, "2") → 成功且更新匹配键。
	var moved capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+a.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"moved"}}`, "迁移匹配键域", pushBoolPtr(true),
			map[string]any{"channelId": "xcpush_tcp", "bizType": "2"}), &moved)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "xcpush_tcp", moved.ChannelId)
	assert.Equal(t, "2", moved.BizType)
	assert.True(t, moved.Enabled)
	assert.True(t, moved.Effective)

	// 原域 (xcpush_tcp, "1") 可开新活跃规则。
	var b capture.PushMockRuleView
	resp = doJSON(t, http.MethodPost, base, pushRuleBody("xcpush_tcp", "1", true), &b)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// 把 B 也迁到 (xcpush_tcp, "2")（A 已在该域）→ 模板池语义：不再 409，
	// 迁移成功且 Effective=true（多条同域共存）。
	var b2 capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+b.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"x"}}`, "迁移到已有活跃规则域", nil,
			map[string]any{"bizType": "2"}), &b2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "2", b2.BizType)
	assert.True(t, b2.Effective)

	// channelId 显式置空 → 400 invalid_field。
	resp = doJSON(t, http.MethodPut, base+"/"+b.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"x"}}`, "置空channel", nil,
			map[string]any{"channelId": ""}), nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 仅迁移匹配键域但不写 note → 400 note_required（2026-10-16 用户拍板：
	// 编辑 push 模板强制备注，对齐 HTTP 规则编辑语义；域迁移算实质编辑）。
	resp = doJSON(t, http.MethodPut, base+"/"+b.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"mocked"}}`, "", nil,
			map[string]any{"bizType": "9"}), nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 仅改 bizType（channelId 未提供）→ 只迁移 bizType 域。
	var c2 capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+b.ID,
		pushUpdateBody("tip", `{"type":"chat","data":{"tip":"y"}}`, "只改bizType", nil,
			map[string]any{"bizType": "3"}), &c2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "xcpush_tcp", c2.ChannelId, "channelId 未提供保持原值")
	assert.Equal(t, "3", c2.BizType)
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

func TestPushMockRuleAPI_SendEmptyPayloadRejected(t *testing.T) {
	// 空 payload 模板（replacePayload 与 replacePayloadBase64 均缺）send → 409
	// push_rule_empty_payload：空指令在 SDK 侧 makePacket 失败且永不 ack，会卡成
	// 永久「待下发」（修复 2026-10-15：PUT 整体覆盖曾把模板 payload 清空）。
	srv := newCaptureTestAPI(t)
	seedMockRuleDevice(t, srv)
	base := srv.URL + "/api/v1/devices/com.example.integrating/dev-1/push-mock-rules"

	body := pushRuleBody("xcpush_tcp", "1", true)
	body["replacePayload"] = ""
	body["replacePayloadBase64"] = ""
	var created capture.PushMockRuleView
	resp := doJSON(t, http.MethodPost, base, body, &created)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	// 回填 payload 后 send → 200（模板即 payload，match 关键字不参与模拟推送形态）。
	update := pushUpdateBody("tip", `{"type":"chat","data":{"tip":"mocked"}}`, "回填 payload", nil)
	var view capture.PushMockRuleView
	resp = doJSON(t, http.MethodPut, base+"/"+created.ID, update, &view)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doJSON(t, http.MethodPost, base+"/"+created.ID+"/send", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
