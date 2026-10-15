package capture

import "testing"

func TestMatchPushPayload_EmptyMatchAlwaysHits(t *testing.T) {
	// 全空条件 = 纯匹配键（channelId+bizType）命中，与 payload 无关。
	if !MatchPushPayload([]byte(`{"a":1}`), PushMockMatch{}) {
		t.Fatal("empty match must always hit")
	}
	if !MatchPushPayload([]byte("binary\x00bytes"), PushMockMatch{}) {
		t.Fatal("empty match must hit even on non-JSON payload")
	}
}

func TestMatchPushPayload_JSONPath(t *testing.T) {
	raw := []byte(`{"type":"chat","data":{"tip":"hello world","count":100,"ok":true}}`)

	// contains（缺省 op）：提取值包含期望串。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathValue: "hello"}) {
		t.Fatal("contains: $.data.tip should contain 'hello'")
	}
	// contains 显式 op。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathOp: PushMatchOpContains, JSONPathValue: "world"}) {
		t.Fatal("contains: $.data.tip should contain 'world'")
	}
	// equals：字符串精确相等。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathOp: PushMatchOpEquals, JSONPathValue: "hello world"}) {
		t.Fatal("equals: $.data.tip == 'hello world'")
	}
	// equals 数字（整数去 .0）。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.count", JSONPathOp: PushMatchOpEquals, JSONPathValue: "100"}) {
		t.Fatal("equals: $.data.count == 100")
	}
	// equals 布尔。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.ok", JSONPathOp: PushMatchOpEquals, JSONPathValue: "true"}) {
		t.Fatal("equals: $.data.ok == true")
	}
	// 未命中：值不匹配 / 提取不到。
	if MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathOp: PushMatchOpEquals, JSONPathValue: "nope"}) {
		t.Fatal("equals mismatch must not hit")
	}
	if MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.missing", JSONPathValue: "x"}) {
		t.Fatal("missing path must not hit")
	}
	// 非法 JSONPath 表达式 → 未命中（不 panic）。
	if MatchPushPayload(raw, PushMockMatch{JSONPath: "$[bad", JSONPathValue: "x"}) {
		t.Fatal("invalid jsonPath must not hit")
	}
	// 数组路径。
	arr := []byte(`{"items":[{"id":"a"},{"id":"b"}]}`)
	if !MatchPushPayload(arr, PushMockMatch{JSONPath: "$.items[1].id", JSONPathOp: PushMatchOpEquals, JSONPathValue: "b"}) {
		t.Fatal("equals: $.items[1].id == b")
	}
}

func TestMatchPushPayload_JSONKeywordFallback(t *testing.T) {
	raw := []byte(`{"type":"chat","data":{"tip":"hello"}}`)
	// JSON + 无 JSONPath + keyword → JSON 原文包含子串。
	if !MatchPushPayload(raw, PushMockMatch{Keyword: "hello"}) {
		t.Fatal("keyword 'hello' must hit JSON payload")
	}
	if MatchPushPayload(raw, PushMockMatch{Keyword: "world"}) {
		t.Fatal("keyword 'world' must not hit")
	}
}

func TestMatchPushPayload_NonJSONFallback(t *testing.T) {
	// 非 JSON payload：keyword 子串匹配。
	raw := []byte("RAW BINARY\x00PAYLOAD-tip-data")
	if !MatchPushPayload(raw, PushMockMatch{Keyword: "tip"}) {
		t.Fatal("keyword must hit non-JSON payload")
	}
	if MatchPushPayload(raw, PushMockMatch{Keyword: "absent"}) {
		t.Fatal("absent keyword must not hit")
	}
	// 非 JSON + 仅 JSONPath（无 keyword）→ 未命中（JSONPath 无法作用于非 JSON）。
	if MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathValue: "x"}) {
		t.Fatal("jsonPath-only must not hit non-JSON payload")
	}
	// 非 JSON + JSONPath + keyword → keyword 分支生效。
	if !MatchPushPayload(raw, PushMockMatch{JSONPath: "$.data.tip", JSONPathValue: "x", Keyword: "tip"}) {
		t.Fatal("keyword fallback must hit when JSONPath inapplicable")
	}
}
