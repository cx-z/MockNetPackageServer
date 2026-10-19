package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
)

func TestRewriteRealPayloadToTemplate_ReplacesDedupFields(t *testing.T) {
	real := `{
		"msgid": 1578813554578165063,
		"session_id": 1573603568714903552,
		"time": 1792034922,
		"touser": 285408282,
		"content": "hi"
	}`
	got := rewriteRealPayloadToTemplate(real)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("转换后应是合法 JSON: %v\n%s", err, got)
	}
	if m["msgid"] != capture.FakePlaceholderMsgID {
		t.Fatalf("msgid 应转占位符，got=%v", m["msgid"])
	}
	if m["session_id"] != capture.FakePlaceholderSessionID {
		t.Fatalf("session_id 应转占位符，got=%v", m["session_id"])
	}
	if m["time"] != capture.FakePlaceholderTime {
		t.Fatalf("time 应转占位符，got=%v", m["time"])
	}
	// 身份字段不动
	if m["touser"].(float64) != 285408282 {
		t.Fatalf("touser 不应被动，got=%v", m["touser"])
	}
}

func TestRewriteRealPayloadToTemplate_NonJSONPassthrough(t *testing.T) {
	in := "not json at all"
	if got := rewriteRealPayloadToTemplate(in); got != in {
		t.Fatalf("非 JSON 应原样返回，got=%s", got)
	}
}

func TestRewriteRealPayloadToTemplate_NoDedupFields(t *testing.T) {
	in := `{"foo":"bar"}`
	if got := rewriteRealPayloadToTemplate(in); got != in {
		t.Fatalf("无判重字段应原样，got=%s", got)
	}
}

func TestInheritIdentityFields_FillsMissing(t *testing.T) {
	// 用户只写了业务内容，没写身份字段
	user := `{"content":"hello","mtype2":169}`
	real := `{
		"touser": 285408282,
		"fromuser": 71,
		"from": {"id":71,"name":"白歧"},
		"session_id": 1573603568714903552,
		"name": "白歧",
		"msgid": 999
	}`
	got, filled := inheritIdentityFields(user, real)
	if len(filled) == 0 {
		t.Fatalf("应补全身份字段，filled=%v", filled)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("合并后应是合法 JSON: %v\n%s", err, got)
	}
	if m["touser"].(float64) != 285408282 {
		t.Fatalf("touser 应被补全，got=%v", m["touser"])
	}
	if m["session_id"].(float64) != 1573603568714903552 {
		t.Fatalf("session_id 应被补全，got=%v", m["session_id"])
	}
	// 用户显式写的业务字段保留
	if m["content"] != "hello" {
		t.Fatalf("用户 content 不应被覆盖，got=%v", m["content"])
	}
	if m["mtype2"].(float64) != 169 {
		t.Fatalf("用户 mtype2 不应被覆盖，got=%v", m["mtype2"])
	}
}

func TestInheritIdentityFields_DoesNotOverrideUserFields(t *testing.T) {
	user := `{"touser":999999,"content":"x"}`
	real := `{"touser":285408282,"fromuser":71}`
	got, _ := inheritIdentityFields(user, real)
	var m map[string]any
	json.Unmarshal([]byte(got), &m)
	if m["touser"].(float64) != 999999 {
		t.Fatalf("用户显式 touser=999999 不应被覆盖，got=%v", m["touser"])
	}
	if m["fromuser"].(float64) != 71 {
		t.Fatalf("fromuser 应从真实包补全，got=%v", m["fromuser"])
	}
}

func TestInheritIdentityFields_NonJSONUserPassthrough(t *testing.T) {
	got, filled := inheritIdentityFields("not json", `{"touser":1}`)
	if got != "not json" || len(filled) != 0 {
		t.Fatalf("用户 payload 非 JSON 应原样返回，got=%s filled=%v", got, filled)
	}
}

func TestInheritIdentityFields_NonJSONRealPassthrough(t *testing.T) {
	got, filled := inheritIdentityFields(`{"content":"x"}`, "garbage")
	if !strings.Contains(got, "content") || len(filled) != 0 {
		t.Fatalf("真实包非 JSON 应不合并，got=%s filled=%v", got, filled)
	}
}

func TestStripSummaryHeader(t *testing.T) {
	// Dokimo 真实格式：summary + " payload=" + JSON
	withHeader := `type=Packet_PacketType(rawValue: 7) msgId=1583744416299470848 bizType=1 payload={"touser":285408282,"content":"hi"}`
	got := stripSummaryHeader(withHeader)
	if !strings.HasPrefix(got, `{"touser"`) {
		t.Fatalf("应剥到 JSON body，got=%s", got)
	}
	// 纯 JSON 原样返回
	pure := `{"touser":1}`
	if got := stripSummaryHeader(pure); got != pure {
		t.Fatalf("纯 JSON 应原样，got=%s", got)
	}
}

func TestInheritIdentityFields_WithSummaryHeader(t *testing.T) {
	// 真实场景：longconn 日志的 payloadText 带摘要头，身份继承仍应生效
	user := `{"mtype2":169,"content":"test"}`
	realWithHeader := `type=Packet_PacketType(rawValue: 7) msgId=1583744416299470848 bizType=1 payload={"touser":285408282,"fromuser":71,"name":"白歧","session_id":1573603568714903552}`
	got, filled := inheritIdentityFields(user, realWithHeader)
	if len(filled) == 0 {
		t.Fatalf("带摘要头的真实包也应补全身份字段，filled=%v", filled)
	}
	var m map[string]any
	json.Unmarshal([]byte(got), &m)
	if m["touser"].(float64) != 285408282 {
		t.Fatalf("touser 应被补全，got=%v", m)
	}
	if m["mtype2"].(float64) != 169 {
		t.Fatalf("用户 mtype2=169 不应被覆盖，got=%v", m)
	}
}

func TestRewriteRealPayloadToTemplate_WithSummaryHeader(t *testing.T) {
	withHeader := `type=Packet msgId=123 bizType=1 payload={"msgid":1578813554578165063,"time":1792034922,"content":"hi"}`
	got := rewriteRealPayloadToTemplate(withHeader)
	var m map[string]any
	json.Unmarshal([]byte(got), &m)
	if m["msgid"] != capture.FakePlaceholderMsgID {
		t.Fatalf("带摘要头时 msgid 也应转占位符，got=%v", m)
	}
	if m["time"] != capture.FakePlaceholderTime {
		t.Fatalf("带摘要头时 time 也应转占位符，got=%v", m)
	}
}
