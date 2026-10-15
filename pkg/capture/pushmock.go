package capture

import "time"

// ============================================================================
// Push mock rules (能力线 L7, push mock 完整规则体系)
// ============================================================================
//
// 与 HTTP MockRule 的关系（L7 决策 D3）：独立模型 + 独立存储 + 独立版本号，
// 复用 HTTP 规则的管理语义（App+设备隔离、显式启停、owner/admin 权限、
// effective 运行时计算、sinceVersion 增量拉取）。HTTP MockRule schema 零改动。
//
// 匹配维度（技术方案 3.4）：channelId 必选 + bizType 可选 + payload 匹配条件。
// 实时拦截在 SDK 本地执行（L7 决策 D1）：SDK 实现轻量匹配子集——每规则最多
// 1 条 JSONPath + 1 个子串条件；Web 一键建规则默认生成子串条件，JSONPath 为
// 高级编辑项。服务端匹配引擎（internal/matching）用于命中统计与 Web 预演。

// PushMatchOp 是 JSONPath 提取值与期望值的比较操作符。
const (
	PushMatchOpEquals   = "equals"   // 提取值 == 期望值
	PushMatchOpContains = "contains" // 提取值包含期望值（默认）
)

// PushMockMatch 是 push mock 规则的 payload 匹配条件（D1 定稿：每规则最多
// 1 条 JSONPath + 1 个子串）。所有条件为空 = 仅按匹配键（channelId+bizType）匹配。
type PushMockMatch struct {
	// JSONPath 是 payload 为 JSON 时的路径表达式（如 "$.data.tip"）；
	// 空 = 不使用 JSONPath 条件。
	JSONPath string `json:"jsonPath,omitempty"`
	// JSONPathOp 是比较操作符（equals | contains）；空按 contains 处理。
	JSONPathOp string `json:"jsonPathOp,omitempty"`
	// JSONPathValue 是 JSONPath 提取值的期望值。
	JSONPathValue string `json:"jsonPathValue,omitempty"`
	// Keyword 是子串退化条件：payload 非 JSON（或无 JSONPath）时，
	// payload 原始字节（解压后）包含 Keyword 即匹配。
	Keyword string `json:"keyword,omitempty"`
}

// PushMockSource 是规则所依据的原 push 日志快照（Web "一键建 Mock 规则"时从
// 一条 LongConnectionRecord 固化；只读展示，不参与匹配——与 MockRuleSource
// 同语义）。临时长连接日志随会话结束清空，此快照随规则持久化保留，供后续
// 查看原始值（Decoded Payload）与 Mock 值对照。
type PushMockSource struct {
	ChannelId     string    `json:"channelId"`
	BizType       string    `json:"bizType,omitempty"`
	MsgId         string    `json:"msgId,omitempty"`
	PayloadText   string    `json:"payloadText,omitempty"`
	PayloadBase64 string    `json:"payloadBase64,omitempty"`
	CapturedAt    time.Time `json:"capturedAt"`
}

// PushMockRule 是持久化的、按设备隔离的 push mock 规则。匹配键 = ChannelId
// （必选）+ BizType（可选，空 = 不限定）；payload 匹配条件与替换 payload 可编辑。
// Enabled 是持久化用户开关；运行时 Effective 标志在读时计算（PushMockRuleView），
// 异常多开状态（同一匹配键多条 enabled）下该域全部不生效。
type PushMockRule struct {
	ID        string        `json:"id"`
	App       string        `json:"app"`
	Did       string        `json:"did"`
	ChannelId string        `json:"channelId"`
	BizType   string        `json:"bizType,omitempty"`
	Match     PushMockMatch `json:"match,omitempty"`
	// ReplacePayload 是替换 payload 的 UTF-8 文本；ReplacePayloadBase64 存在时
	// 仅作展示（对齐 MockResponse.Body/BodyBase64 语义）。
	ReplacePayload string `json:"replacePayload,omitempty"`
	// ReplacePayloadBase64 是替换 payload 的原始字节（base64）——二进制/压缩
	// 包时使用；SDK 解码后写 Packet.bizData。
	ReplacePayloadBase64 string          `json:"replacePayloadBase64,omitempty"`
	Enabled              bool            `json:"enabled"`
	Source               *PushMockSource `json:"source,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
	// LastUsedAt 是滑动窗口清理基线（同 MockRule）：命中、Web 编辑保存、
	// Web toggle 启停三类操作刷新；心跳/版本轮询/纯浏览不刷新。
	LastUsedAt time.Time `json:"lastUsedAt"`
	// HitCount 是命中次数累计（SDK 旁路采集上报 marked=true 时 +1）。
	HitCount int64 `json:"hitCount"`
	// LastHitAt 是最近一次命中时间（marked 上报刷新）。
	LastHitAt time.Time `json:"lastHitAt"`
	// Note 是规则备注。纯管理面字段，不参与匹配、不下发给 SDK。
	Note string `json:"note,omitempty"`
	// Owner 是规则创建者用户名（服务端写入）。纯服务端字段，SDK 增量拉取不下发。
	Owner string `json:"owner,omitempty"`
	// UpdatedBy 是最后修改者用户名。纯服务端字段，SDK 增量拉取不下发。
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// PushMockRuleInput 是创建 payload（对齐 MockRuleInput）。创建定义匹配键
// （ChannelId 必选 + BizType 可选）；编辑用 UpdatePushMockRuleInput，匹配键 frozen。
type PushMockRuleInput struct {
	ChannelId            string          `json:"channelId"`
	BizType              string          `json:"bizType,omitempty"`
	Match                PushMockMatch   `json:"match,omitempty"`
	ReplacePayload       string          `json:"replacePayload,omitempty"`
	ReplacePayloadBase64 string          `json:"replacePayloadBase64,omitempty"`
	Enabled              bool            `json:"enabled"`
	Note                 string          `json:"note,omitempty"`
	Source               *PushMockSource `json:"source,omitempty"`
}

// UpdatePushMockRuleInput 是编辑 payload（对齐 UpdateMockRuleInput）。
// 不含 ChannelId/BizType（匹配键不可改），不含 Source（快照只读）。允许改
// payload 匹配条件、替换 payload、note、enabled。
type UpdatePushMockRuleInput struct {
	Match                PushMockMatch `json:"match,omitempty"`
	ReplacePayload       string        `json:"replacePayload,omitempty"`
	ReplacePayloadBase64 string        `json:"replacePayloadBase64,omitempty"`
	Note                 string        `json:"note,omitempty"`
	Enabled              *bool         `json:"enabled,omitempty"`
}

// PushMockRuleView 是 API 输出：持久化规则 + 服务端计算的运行时 Effective 标志。
// SDK 只应用 Effective=true 的规则。
type PushMockRuleView struct {
	*PushMockRule
	// Effective 仅当该规则是其匹配键（channelId+bizType）域内唯一 enabled 时为 true。
	// 异常多开状态下该域全部规则 Effective=false 并在 conflicts 报告。
	Effective bool `json:"effective"`
}

// PushMockRuleConflict 报告异常状态：同一匹配键（channelId+bizType）存在多条
// enabled 规则；整个域不生效，Web 弹窗提示（对齐 MockRuleConflict）。
type PushMockRuleConflict struct {
	ChannelId string `json:"channelId"`
	BizType   string `json:"bizType"`
	Message   string `json:"message"`
}

// PushRuleCommand 是一次「模拟推送」指令（能力线 L7 模拟推送形态，2026-10-14
// 用户拍板 D0）：Web 点「模拟推送」→ 服务端按设备入队 → SDK 心跳响应携带 →
// SDK 构造 Packet 注入业务（消费前）→ SDK ack 后标记消费。指令携带规则 payload
// 快照（规则可能在下发前被编辑，快照保证下推内容一致）。指令独立于规则集，
// 不 bump PushRuleVersions（临时队列语义）；同一规则重复触发每次追加新指令
// （支持「连续模拟推送多条」验证）。
type PushRuleCommand struct {
	ID            string    `json:"id"`
	RuleID        string    `json:"ruleId"`
	ChannelId     string    `json:"channelId"`
	BizType       string    `json:"bizType,omitempty"`
	Payload       string    `json:"payload,omitempty"`
	PayloadBase64 string    `json:"payloadBase64,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	Consumed      bool      `json:"consumed"`
	ConsumedAt    time.Time `json:"consumedAt,omitempty"`
}

// PushRuleCommandView 是 SDK 线格式的指令视图（心跳响应携带）：剥离 Consumed/
// ConsumedAt（消费标记由 SDK ack 端点回报），保留构造 Packet 所需字段。
type PushRuleCommandView struct {
	ID            string `json:"id"`
	RuleID        string `json:"ruleId"`
	ChannelId     string `json:"channelId"`
	BizType       string `json:"bizType,omitempty"`
	Payload       string `json:"payload,omitempty"`
	PayloadBase64 string `json:"payloadBase64,omitempty"`
}
