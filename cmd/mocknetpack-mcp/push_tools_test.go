package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getmockd/mockd/pkg/capture"
)

// TestMCPServer_ListPushMockRulesTool asserts list_push_mock_rules returns the
// slim per-template view (payload preview, not the full payload) and supports
// channelId filtering.
func TestMCPServer_ListPushMockRulesTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		writeResp(w, 200, map[string]any{"version": 3, "rules": []*capture.PushMockRuleView{
			{PushMockRule: &capture.PushMockRule{ID: "pr1", ChannelId: "xcpush_tcp", BizType: "1",
				ReplacePayload: `{"content":"hello"}`, Enabled: true, Note: "chat", HitCount: 7}, Effective: true},
			{PushMockRule: &capture.PushMockRule{ID: "pr2", ChannelId: "xcpush_tcp", BizType: "2",
				ReplacePayload: `{"type":"mock"}`, Enabled: false, Note: "off"}, Effective: false},
		}})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "list_push_mock_rules", map[string]any{"app": "com.a", "did": "d1"})
	assert.False(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, `"ruleId": "pr1"`)
	assert.Contains(t, body, `"channelId": "xcpush_tcp"`)
	assert.Contains(t, body, `"hitCount": 7`)
	assert.Contains(t, body, `"enabled": true`)
	assert.Contains(t, body, `"version": 3`)

	// channelId filter keeps only the matching channel.
	res = callTool(t, srv, "list_push_mock_rules", map[string]any{"app": "com.a", "did": "d1", "channelId": "xcpush_tcp"})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "pr1")
}

// TestMCPServer_CreatePushMockRuleTool asserts create_push_mock_rule sends
// channelId/bizType/replacePayload with enabled=true (一键建模板语义) and that
// note is mandatory for a source-less template (server ErrNoteRequired).
func TestMCPServer_CreatePushMockRuleTool(t *testing.T) {
	var got capture.PushMockRuleInput
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeResp(w, 201, capture.PushMockRuleView{PushMockRule: &capture.PushMockRule{
			ID: "pr-new", ChannelId: got.ChannelId, BizType: got.BizType,
			ReplacePayload: got.ReplacePayload, Enabled: got.Enabled, Note: got.Note,
		}, Effective: got.Enabled})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "create_push_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "channelId": "xcpush_tcp", "bizType": "1",
		"replacePayload": `{"content":"mock-msg"}`, "note": "复现压测",
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Equal(t, "xcpush_tcp", got.ChannelId)
	assert.Equal(t, "1", got.BizType)
	assert.Equal(t, `{"content":"mock-msg"}`, got.ReplacePayload)
	assert.Equal(t, "复现压测", got.Note)
	assert.True(t, got.Enabled, "created template must be enabled so send works immediately")
	body := resultText(res)
	assert.Contains(t, body, `"ruleId": "pr-new"`)
	assert.Contains(t, body, "已创建且启用")

	// missing channelId → usage error before any HTTP call.
	res = callTool(t, srv, "create_push_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "replacePayload": "{}", "note": "n",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "channelId 必填")

	// missing note → usage error (server would reject with ErrNoteRequired).
	res = callTool(t, srv, "create_push_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "channelId": "xcpush_tcp", "replacePayload": "{}",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "note 必填")
}

// TestMCPServer_UpdatePushMockRuleTool asserts the read-modify-write contract:
// only replacePayload changes; match/note survive; a binary template switched
// to a text payload clears replacePayloadBase64 (SDK constructs from base64
// first, so the stale snapshot must not linger).
func TestMCPServer_UpdatePushMockRuleTool(t *testing.T) {
	existing := `{"id":"pr1","channelId":"xcpush_tcp","bizType":"1",
		"match":{"keyword":"old"},"replacePayload":"(binary)","replacePayloadBase64":"AAEC",
		"enabled":true,"note":"keep-me","hitCount":3}`
	var got capture.UpdatePushMockRuleInput
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		writeResp(w, 200, map[string]any{"version": 3, "rules": []json.RawMessage{json.RawMessage(existing)}})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules/pr1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeResp(w, 200, capture.PushMockRuleView{PushMockRule: &capture.PushMockRule{
			ID: "pr1", ChannelId: "xcpush_tcp", BizType: "1",
			Match: got.Match, ReplacePayload: got.ReplacePayload,
			ReplacePayloadBase64: got.ReplacePayloadBase64,
			Enabled:              true, Note: got.Note,
		}, Effective: true})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "update_push_mock_rule", map[string]any{
		"app": "com.a", "did": "d1", "ruleId": "pr1",
		"replacePayload": `{"content":"new-mock"}`,
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Equal(t, `{"content":"new-mock"}`, got.ReplacePayload)
	assert.Equal(t, "keep-me", got.Note, "existing note must be preserved (server rejects blank note on content change)")
	assert.Equal(t, "old", got.Match.Keyword, "match must be re-sent unchanged (full-replace PUT)")
	assert.Empty(t, got.ReplacePayloadBase64, "binary snapshot must be cleared when switching to a text payload")
	body := resultText(res)
	assert.Contains(t, body, `"payloadMode": "text"`)
}

// TestMCPServer_DeletePushMockRuleTool asserts delete issues DELETE and reports
// success.
func TestMCPServer_DeletePushMockRuleTool(t *testing.T) {
	var deleted bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules/pr1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "delete_push_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "pr1"})
	assert.False(t, res.IsError, resultText(res))
	assert.True(t, deleted)
	assert.Contains(t, resultText(res), "已删除")
}

// TestMCPServer_SendPushMockRuleTool asserts send enqueues one pending command
// and its result wording keeps "enqueue ≠ delivered"; a disabled rule surfaces
// the 409 guidance.
func TestMCPServer_SendPushMockRuleTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules/pr1/send", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		writeResp(w, 200, capture.PushRuleCommandView{ID: "cmd-1", RuleID: "pr1", ChannelId: "xcpush_tcp", BizType: "1"})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules/pr2/send", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 409, map[string]string{"error": "push_rule_disabled", "message": "推送模板已停用"})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "send_push_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "pr1"})
	assert.False(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, `"commandId": "cmd-1"`)
	assert.Contains(t, body, "入队")
	assert.Contains(t, body, "已到达设备")

	// disabled rule → 409 guidance.
	res = callTool(t, srv, "send_push_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "pr2"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "停用")
}

// TestMCPServer_GetLongConnPacketSamplesTool asserts the two-step orchestration
// (session resolution → packet log) filters out mocked fake packets, keeps the
// full payloadText skeleton, honors bizType and limit, and explains the empty
// session case.
func TestMCPServer_GetLongConnPacketSamplesTool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	mux.HandleFunc("/api/v1/sessions/s1/longconn", func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "eventType=packet")
		assert.Contains(t, r.URL.RawQuery, "bizType=1")
		assert.Contains(t, r.URL.RawQuery, "limit=50")
		writeResp(w, 200, map[string]any{"entries": []capture.LongConnectionRecord{
			{ID: "lc1", SessionID: "s1", Seq: 1, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m1", PayloadText: `{"content":"real-1"}`, Mocked: false,
			}},
			{ID: "lc2", SessionID: "s1", Seq: 2, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m2", PayloadText: `{"content":"fake"}`, Mocked: true, MockRuleID: "pr1",
			}},
			{ID: "lc3", SessionID: "s1", Seq: 3, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m3", PayloadText: `{"content":"real-2"}`, Mocked: false,
			}},
		}, "total": 3})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "get_longconn_packet_samples", map[string]any{
		"app": "com.a", "did": "d1", "bizType": "1", "limit": float64(5),
	})
	assert.False(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, `"seq": 1`)
	assert.Contains(t, body, "real-1")
	assert.Contains(t, body, "real-2")
	assert.NotContains(t, body, `"content":"fake"`, "mocked fake packets must be filtered out of the skeleton reference")
	assert.Contains(t, body, "骨架仅供构造模板 payload 参考")

	// No session → guidance text, not an error.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{"sessions": []capture.CaptureSession{}, "total": 0})
	})
	_, base2 := setupFake(t, mux2)
	srv2 := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv2, base2)
	res = callTool(t, srv2, "get_longconn_packet_samples", map[string]any{"app": "com.a", "did": "d1"})
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(res), "没有任何长连接日志会话")
}

// TestMCPServer_ListLongConnLogsTool asserts the generic log list: query params
// pass through, entries are slim-projected by default, mocked fake packets stay
// visible (unlike packet samples), includePayload adds full payload, and the
// latest snapshot event is surfaced as latestSnapshot.
func TestMCPServer_ListLongConnLogsTool(t *testing.T) {
	var lastRawQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	mux.HandleFunc("/api/v1/sessions/s1/longconn", func(w http.ResponseWriter, r *http.Request) {
		lastRawQuery = r.URL.RawQuery
		writeResp(w, 200, map[string]any{"entries": []capture.LongConnectionRecord{
			{ID: "lc1", SessionID: "s1", Seq: 1, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m1", PayloadText: `{"content":"real-1"}`, Mocked: false,
			}},
			{ID: "lc2", SessionID: "s1", Seq: 2, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m2", PayloadText: `{"content":"fake"}`, Mocked: true, MockRuleID: "pr1",
			}},
			{ID: "lc3", SessionID: "s1", Seq: 3, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventSnapshot, Status: "connected",
			}},
		}, "total": 3})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "list_longconn_logs", map[string]any{
		"app": "com.a", "did": "d1", "eventType": "packet", "bizType": "1", "limit": float64(50),
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, lastRawQuery, "eventType=packet")
	assert.Contains(t, lastRawQuery, "bizType=1")
	assert.Contains(t, lastRawQuery, "limit=50")
	body := resultText(res)
	assert.Contains(t, body, `"sessionId": "s1"`)
	assert.Contains(t, body, `"seq": 1`)
	assert.Contains(t, body, `"seq": 2`)
	assert.Contains(t, body, `"mocked": true`, "log list keeps fake packets visible (samples tool filters them)")
	assert.Contains(t, body, `"latestSnapshot"`)
	assert.NotContains(t, body, `"payloadText"`, "default projection must not carry full payloads")

	// includePayload=true exposes full payloadText.
	res = callTool(t, srv, "list_longconn_logs", map[string]any{
		"app": "com.a", "did": "d1", "includePayload": true, "limit": float64(50),
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), `"payloadText"`)

	// Invalid eventType → usage error.
	res = callTool(t, srv, "list_longconn_logs", map[string]any{
		"app": "com.a", "did": "d1", "eventType": "bogus",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "eventType 取值无效")
}

// TestMCPServer_CreatePushMockRuleFromLogTool asserts the two-step orchestration:
// session resolution → paging to the logId → template creation with source
// snapshot (channelId/bizType/msgId), enabled=true, and payload override support.
func TestMCPServer_CreatePushMockRuleFromLogTool(t *testing.T) {
	var got capture.PushMockRuleInput
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	mux.HandleFunc("/api/v1/sessions/s1/longconn", func(w http.ResponseWriter, r *http.Request) {
		writeResp(w, 200, map[string]any{"entries": []capture.LongConnectionRecord{
			{ID: "lc1", SessionID: "s1", Seq: 1, LongConnectionRecordInput: capture.LongConnectionRecordInput{
				ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, BizType: "1",
				MsgID: "m1", PayloadText: `{"content":"real-1"}`,
				TS: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC),
			}},
		}, "total": 1})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeResp(w, 201, capture.PushMockRuleView{PushMockRule: &capture.PushMockRule{
			ID: "pr-from-log", ChannelId: got.ChannelId, BizType: got.BizType,
			ReplacePayload: got.ReplacePayload, Enabled: got.Enabled, Note: got.Note,
			Source: got.Source,
		}, Effective: true})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "create_push_mock_rule_from_log", map[string]any{
		"app": "com.a", "did": "d1", "logId": "lc1", "note": "复现 chat 场景",
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Equal(t, "xcpush_tcp", got.ChannelId, "channelId must come from the log record")
	assert.Equal(t, "1", got.BizType, "bizType must come from the log record")
	assert.Equal(t, `{"content":"real-1"}`, got.ReplacePayload, "payload must default to the record payloadText")
	assert.True(t, got.Enabled, "one-click template must be enabled")
	assert.Equal(t, "复现 chat 场景", got.Note)
	require.NotNil(t, got.Source, "one-click creation must carry the source snapshot")
	assert.Equal(t, "xcpush_tcp", got.Source.ChannelId)
	assert.Equal(t, "1", got.Source.BizType)
	assert.Equal(t, "m1", got.Source.MsgId)
	assert.Equal(t, `{"content":"real-1"}`, got.Source.PayloadText, "source snapshot must carry payloadText")
	assert.Equal(t, time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC), got.Source.CapturedAt, "source snapshot must carry capturedAt")
	body := resultText(res)
	assert.Contains(t, body, `"ruleId": "pr-from-log"`)
	assert.Contains(t, body, "一键建模板")

	// replacePayload override wins over the record payload.
	got = capture.PushMockRuleInput{}
	res = callTool(t, srv, "create_push_mock_rule_from_log", map[string]any{
		"app": "com.a", "did": "d1", "logId": "lc1", "replacePayload": `{"content":"override"}`,
	})
	assert.False(t, res.IsError, resultText(res))
	assert.Equal(t, `{"content":"override"}`, got.ReplacePayload)
	assert.Equal(t, "from log lc1", got.Note, "missing note must auto-fill 'from log <logId>' to avoid update 400")

	// Unknown logId → guidance error.
	res = callTool(t, srv, "create_push_mock_rule_from_log", map[string]any{
		"app": "com.a", "did": "d1", "logId": "nope",
	})
	assert.False(t, res.IsError) // guidance is a text result, not an error
	assert.Contains(t, resultText(res), "未找到日志记录")
}

// TestMCPServer_GetPushMockRuleTool asserts the full-template detail view:
// complete payload (not truncated), source snapshot, hit counts.
func TestMCPServer_GetPushMockRuleTool(t *testing.T) {
	capturedAt := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/push-mock-rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		writeResp(w, 200, map[string]any{"version": 5, "rules": []*capture.PushMockRuleView{
			{PushMockRule: &capture.PushMockRule{ID: "pr1", ChannelId: "xcpush_tcp", BizType: "1",
				ReplacePayload: `{"content":"very-long-payload-that-must-not-be-truncated"}`,
				Enabled: true, Note: "chat", HitCount: 9, Owner: "admin",
				Source: &capture.PushMockSource{ChannelId: "xcpush_tcp", BizType: "1", MsgId: "m1", CapturedAt: capturedAt},
			}, Effective: true},
		}})
	})
	_, base := setupFake(t, mux)
	srv := server.NewMCPServer("mocknetpack", "0.13.0")
	registerTools(srv, base)

	res := callTool(t, srv, "get_push_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "pr1"})
	assert.False(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, "very-long-payload-that-must-not-be-truncated", "detail view must not truncate the payload")
	assert.Contains(t, body, `"hitCount": 9`)
	assert.Contains(t, body, `"msgId": "m1"`)
	assert.Contains(t, body, `"capturedAt"`)

	// Unknown ruleId → 404 guidance.
	res = callTool(t, srv, "get_push_mock_rule", map[string]any{"app": "com.a", "did": "d1", "ruleId": "missing"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "不存在")
}
