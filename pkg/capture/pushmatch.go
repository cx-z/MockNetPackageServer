package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ohler55/ojg/jp"
)

// ============================================================================
// Push mock payload matching
// ============================================================================
//
// 服务端匹配引擎：用于 Web 预演/命中统计语义对齐与 SDK 轻量子集的参考实现。
// 实时拦截在 SDK 本地执行，SDK 按本函数的语义实现轻量匹配。

// MatchPushPayload evaluates a rule's PushMockMatch against the raw payload
// bytes of an inbound push (解压后原始字节，非 base64)。
//
// 匹配语义（技术方案 3.4）：
//   - payload 为 JSON：
//     - JSONPath 非空 → 提取路径值后按 op 比较（equals=字符串化相等，
//       contains=字符串化包含，缺省 contains）；提取不到值 → 未命中
//     - JSONPath 为空且 Keyword 非空 → JSON 原文包含 keyword（子串退化）
//   - payload 非 JSON：Keyword 非空 → 原始字节包含 keyword；否则未命中
//     （JSONPath 无法作用于非 JSON payload）
//   - 所有条件为空 → 命中（纯 channelId+bizType 匹配键）
func MatchPushPayload(raw []byte, m PushMockMatch) bool {
	if m.JSONPath == "" && m.Keyword == "" {
		return true
	}
	if json.Valid(raw) {
		if m.JSONPath != "" {
			return matchJSONPathValue(raw, m)
		}
		return bytes.Contains(raw, []byte(m.Keyword))
	}
	if m.Keyword != "" {
		return bytes.Contains(raw, []byte(m.Keyword))
	}
	return false
}

// matchJSONPathValue extracts the value(s) at m.JSONPath and compares against
// m.JSONPathValue using the op (equals | contains, 缺省 contains)。
func matchJSONPathValue(raw []byte, m PushMockMatch) bool {
	expr, err := jp.ParseString(m.JSONPath)
	if err != nil {
		return false
	}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return false
	}
	results := expr.Get(data)
	if len(results) == 0 {
		return false
	}
	op := m.JSONPathOp
	if op == "" {
		op = PushMatchOpContains
	}
	for _, r := range results {
		s := stringifyJSONValue(r)
		switch op {
		case PushMatchOpEquals:
			if s == m.JSONPathValue {
				return true
			}
		default: // contains
			if strings.Contains(s, m.JSONPathValue) {
				return true
			}
		}
	}
	return false
}

// stringifyJSONValue 将 JSON 提取值字符串化用于比较：字符串原样、数字/布尔
// 用其 JSON 表示（整数去 ".0"：100 而非 100.0）、null 为空串、对象/数组
// JSON 序列化。
func stringifyJSONValue(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return fmt.Sprintf("%v", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
