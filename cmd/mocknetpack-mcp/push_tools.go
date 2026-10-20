package main

// push mock MCP tools (模拟推送形态): list_push_mock_rules,
// create_push_mock_rule, update_push_mock_rule, delete_push_mock_rule,
// send_push_mock_rule, get_longconn_packet_samples.
//
// 形态语义（写进每个工具 description，避免 AI 误用）：
//   - push mock 是「一次性指令」：点 send 即入队一条 pending 指令，无启停开关
//     概念（规则自身的 enabled 只是「能否 send」的门槛，创建即启用）。因此这里
//     不提供 set_push_mock_rule_enabled 形态的工具，也不照抄 HTTP mock 规则的
//     enabled/enable/disable 工具形态。
//   - send 返回的是「指令已入队」，SDK 约 3s 心跳拉取后才构造假 Packet 注入
//     业务——入队成功 ≠ 已到达设备。
//   - 真机血泪教训（payload 骨架约束）：模板 payload 不能凭字段名从零编，必须
//     先 get_longconn_packet_samples 拉同 bizType 的真实日志作骨架再改内层文本；
//     msgid/session_id/time 每次换成新随机大整数防判重；touser/fromuser/name
//     继承当前账号真实会话骨架、不能跨账号抄。
//
// 所有 handler 复用既有 HTTP 端点（薄封装）：push mock CRUD/send 走
// /devices/{app}/{did}/push-mock-rules*，日志骨架走 /sessions/{id}/longconn
// （会话解析两步编排，同 get_device_traffic 模式）。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
)

// pushPayloadPreview 把模板 payload 截断成 AI 可读的摘要（列表投影用，完整
// payload 用 get_push_mock_rule 或 update 前的 read 拿）。
func pushPayloadPreview(p string) string {
	const max = 120
	if len(p) <= max {
		return p
	}
	return p[:max] + "…"
}

// identityFields 是「凭空构造兜底」时从真实包继承的身份字段白名单。
// 这些是账号/会话维度字段（不是业务维度），同设备同账号任意一条真实包都能提供。
var identityFields = []string{
	"touser", "fromuser", "from", "name",
	"session_id", "session_type", "avatar", "gender", "official",
}

// stripSummaryHeader 剥掉长连接日志 payloadText 前面的摘要头。
// LongConnHook 拼的格式是 "<summary> payload=<JSON body>"（summary 由业务方
// 拼装，如 "type=Packet... msgId=... bizType=1"）。MCP 层做 JSON 解析前必须剥掉，
// 否则 json.Unmarshal 在开头就失败，身份继承/占位符转换静默不生效。
// 找不到 " payload=" 分隔符时原样返回（payload 本就是纯 JSON 的情况）。
func stripSummaryHeader(payloadText string) string {
	const marker = " payload="
	idx := strings.Index(payloadText, marker)
	if idx < 0 {
		return payloadText
	}
	return strings.TrimSpace(payloadText[idx+len(marker):])
}

// rewriteRealPayloadToTemplate 把从真实日志复制的 payload 转成带占位符的模板：
// 顶层 msgid/session_id/time 数字字段的值换成 "${fake_*}" 字符串占位符。
// 入队时 capture.RotateFakePlaceholders 会把带引号占位符替换成裸数字，保持
// IM payload 的数字类型。非 JSON / 字段缺失时原样返回。
func rewriteRealPayloadToTemplate(payload string) string {
	body := stripSummaryHeader(payload)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return payload
	}
	changed := false
	for k, ph := range map[string]string{
		"msgid":      capture.FakePlaceholderMsgID,
		"session_id": capture.FakePlaceholderSessionID,
		"time":       capture.FakePlaceholderTime,
	} {
		if _, ok := m[k]; ok {
			m[k] = ph
			changed = true
		}
	}
	if !changed {
		return payload
	}
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return string(out)
}

// inheritIdentityFields 从真实包 realPayload 提取身份字段，shallow merge 进
// 用户手写的 userPayload。用户已显式提供的字段不覆盖，只补缺失的。
// 返回合并后的 payload 文本，以及实际补了哪些字段（用于 hint 审计）。
// 任一 payload 非 JSON 时返回原 userPayload（不强行合并）。
func inheritIdentityFields(userPayload, realPayload string) (string, []string) {
	var user, real map[string]any
	if err := json.Unmarshal([]byte(userPayload), &user); err != nil {
		return userPayload, nil
	}
	if err := json.Unmarshal([]byte(stripSummaryHeader(realPayload)), &real); err != nil {
		return userPayload, nil
	}
	var filled []string
	for _, k := range identityFields {
		if _, userHas := user[k]; userHas {
			continue
		}
		if v, ok := real[k]; ok {
			user[k] = v
			filled = append(filled, k)
		}
	}
	if len(filled) == 0 {
		return userPayload, nil
	}
	out, err := json.Marshal(user)
	if err != nil {
		return userPayload, nil
	}
	return string(out), filled
}

// ---- tool definitions (schemas) -------------------------------------------------

func toolListPushMockRules() mcp.Tool {
	return mcp.NewTool("list_push_mock_rules",
		mcp.WithDescription("列出该设备全部推送模板摘要（ruleId/channelId/bizType/replacePayload 摘要/enabled/effective/hitCount/note），支持按 channelId 筛选。"+
			"何时用：找 ruleId、确认模板能否 send（enabled=true 才能模拟推送）、检查是否已有同通道模板。"+
			"注意：模板是「一次性 payload 快照」，无启停开关概念；创建即启用。错误处理：403 仅 owner/admin 可见。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("channelId", mcp.Description("按通道标识精确筛选（如 xcpush_tcp）；不传返回全部")),
	)
}

func toolCreatePushMockRule() mcp.Tool {
	return mcp.NewTool("create_push_mock_rule",
		mcp.WithDescription("凭空创建推送模板（无日志来源）：channelId 必填 + replacePayload 必填 + note 必填。创建即启用（enabled=true），可直接 send_push_mock_rule。"+
			"何时用：模拟一条未抓包过的推送（冷启动场景）。"+
			"平台已兜底两件事（无需手动处理）：①身份字段自动继承——本工具会从该设备最近一条真实 packet 自动补全 touser/fromuser/from/name/session_id 等你没写的字段（你显式写的不覆盖）；②判重 id 自动轮换——模板里写 \"msgid\": \"${fake_msgid}\"、\"time\": \"${fake_time}\" 等占位符，send 时自动换新值防判重（不写占位符则原样下发，重复 send 会被客户端判重吞掉）。"+
			"你只需要关心业务内层内容（如 content/say_content 里的展示文本、mtype/mtype2 等业务分支字段）。"+
			"错误处理：400 channelId/note 缺失；403 仅 owner/admin。模板池语义：同 channelId+bizType 域允许多条模板共存、全部生效。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("channelId", mcp.Required(), mcp.Description("通道标识（必填，如 xcpush_tcp）")),
		mcp.WithString("bizType", mcp.Description("业务类型（可选，如 1=IM 聊天；空=不限定）")),
		mcp.WithString("replacePayload", mcp.Required(), mcp.Description("推送 payload 模板（JSON 文本；身份字段缺省会自动补全，msgid/time 用 ${fake_msgid}/${fake_time} 占位符自动轮换）")),
		mcp.WithString("note", mcp.Required(), mcp.Description("规则备注（必填且非空白——服务端要求无 source 快照的手动模板必须带备注，如\"复现 subtype=169\"）")),
	)
}

func toolUpdatePushMockRule() mcp.Tool {
	return mcp.NewTool("update_push_mock_rule",
		mcp.WithDescription("编辑推送模板的 replacePayload（模板 payload）。read-modify-write：只传新 payload 即可，note/其他字段自动保留；"+
			"若原模板是 base64 二进制 payload，传 replacePayload 后自动转为文本模式。"+
			"本工具不改匹配键域（channelId/bizType 保持原值）；需要迁移域或改备注请用 Web 编辑面板（模板池语义：同域允许多条共存，改域不再有 409 冲突）。"+
			"何时用：改模板 payload 后重新 send（模板本身无启停开关，enabled 仅作 send 门槛）。错误处理：403 仅 owner/admin；404 模板不存在。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("模板 ID（来自 list_push_mock_rules）")),
		mcp.WithString("replacePayload", mcp.Required(), mcp.Description("新的推送 payload 模板（必填）")),
	)
}

func toolDeletePushMockRule() mcp.Tool {
	return mcp.NewTool("delete_push_mock_rule",
		mcp.WithDescription("删除一条推送模板（立即生效：该模板不再可 send；已入队的 pending 指令不受影响，SDK 仍会消费）。何时用：清理测试模板。错误处理：403 仅 owner/admin 可删；404 模板不存在。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("模板 ID（来自 list_push_mock_rules）")),
	)
}

func toolSendPushMockRule() mcp.Tool {
	return mcp.NewTool("send_push_mock_rule",
		mcp.WithDescription("模拟推送：为设备入队一条 pending 指令（快照模板 payload），SDK 约 3s 心跳拉取后构造假 Packet 走真实 delegate 回调注入业务。"+
			"**入队成功 ≠ 已到达设备**——本工具返回的是指令已入队；到达证据是稍后长连接日志出现 mocked=true 的 packet（get_longconn_packet_samples 或 Web 长连接页签）。"+
			"同一模板可重复 send 连续下发多条；不影响任何真实 push。"+
			"何时用：让业务收到一条 mock push。错误处理：409 push_rule_disabled 模板停用（MCP 无启停开关：可 delete 后重建，或在 Web 端启用）；403 仅规则 owner/admin；404 模板不存在。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("模板 ID（来自 list_push_mock_rules）")),
	)
}

func toolGetLongConnPacketSamples() mcp.Tool {
	return mcp.NewTool("get_longconn_packet_samples",
		mcp.WithDescription("取该设备最近真实长连接 packet 日志（mocked=false，模拟推送产生的 fake 包已过滤），返回 bizType + 完整 payloadText 骨架。"+
			"这是给 AI 构造推送模板 payload 用的「骨架参考」：真实 payload 结构复杂，不要凭字段名从零编——"+
			"以 com.pixelmuse.dokimo 的 bizType=1（IM 聊天）为例：顶层 content 是 JSON 字符串（say_content 在其内层），touser/fromuser/name 是数字 id + 嵌套 from 对象。"+
			"用骨架改模板时：msgid/session_id/time 必须每次换成全新随机大整数（否则 IM 客户端按 id 判重，假 push 被静默丢弃、UI 无变化）；"+
			"touser/fromuser/name 必须继承该设备当前登录账号的真实会话骨架，不能跨账号抄（否则业务 ChatMessageService 判定无此会话直接丢弃）。"+
			"何时用：create_push_mock_rule / update_push_mock_rule 之前的必调步骤（按 bizType 过滤取同业务骨架）。"+
			"错误处理：无会话或无 packet 日志时返回空列表与提示。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("bizType", mcp.Description("按业务类型精确过滤（建议传目标业务类型，如 1=IM 聊天）；不传返回全部业务")),
		mcp.WithNumber("limit", mcp.Description("最多返回的真实骨架条数（默认 5，最大 20）；内部会多拉后过滤 mocked，不足时返回实际数量")),
	)
}

// ---- tool handlers ------------------------------------------------------------

func mcpListPushMockRules(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		channelFilter := ""
		if v, ok := p["channelId"].(string); ok {
			channelFilter = v
		}
		list, err := c.ListPushMockRules(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}
		views := make([]map[string]any, 0, len(list.Rules))
		for _, r := range list.Rules {
			if r == nil || r.PushMockRule == nil {
				continue
			}
			if channelFilter != "" && r.ChannelID != channelFilter {
				continue
			}
			payload := r.ReplacePayload
			if payload == "" {
				payload = r.ReplacePayloadBase64
			}
			views = append(views, map[string]any{
				"ruleId":         r.ID,
				"channelId":      r.ChannelID,
				"bizType":        r.BizType,
				"payloadPreview": pushPayloadPreview(payload),
				"enabled":        r.Enabled,
				"effective":      r.Effective,
				"hitCount":       r.HitCount,
				"note":           r.Note,
			})
		}
		out := map[string]any{"version": list.Version, "rules": views, "total": len(views)}
		return jsonText(out), nil
	}
}

func mcpCreatePushMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		in := &capture.PushMockRuleInput{
			ChannelID:      mustString(p, "channelId"),
			BizType:        mustString(p, "bizType"),
			ReplacePayload: mustString(p, "replacePayload"),
			Note:           mustString(p, "note"),
			Enabled:        true, // 一键建模板语义：创建即启用，可直接 send
		}
		if in.ChannelID == "" {
			return mcp.NewToolResultError("channelId 必填（通道标识，如 xcpush_tcp）。"), nil
		}
		if strings.TrimSpace(in.ReplacePayload) == "" {
			return mcp.NewToolResultError("replacePayload 必填（推送 payload 模板）。建议先用 get_longconn_packet_samples 拉真实骨架再改内层文本。"), nil
		}
		if strings.TrimSpace(in.Note) == "" {
			return mcp.NewToolResultError("note 必填且非空白（服务端要求无 source 快照的手动模板必须带备注）。"), nil
		}

		// 凭空构造兜底：手写模板无 Source，自动从该设备最近一条真实 packet
		// 继承身份字段（touser/fromuser/from/name/session_id 等），用户已显式
		// 提供的字段不覆盖。best-effort：拉不到真实包不阻塞创建。
		var filledFields []string
		if sample, ok := latestRealPacketForIdentity(ctx, c, app, did); ok {
			merged, filled := inheritIdentityFields(in.ReplacePayload, sample)
			in.ReplacePayload = merged
			filledFields = filled
		}

		rule, err := c.CreatePushMockRule(ctx, app, did, in)
		if err != nil {
			return toolErr(err), nil
		}
		var hint string
		if len(filledFields) > 0 {
			hint = "已从设备最近真实 packet 自动补全身份字段：" + strings.Join(filledFields, ", ") +
				"（你显式提供的字段未覆盖）。模板里 ${fake_msgid}/${fake_time} 会在 send 时自动轮换。" +
				"模拟推送请调用 send_push_mock_rule(ruleId)。"
		} else {
			hint = "未找到该设备真实 packet 可继承身份字段——请自行确保 touser/fromuser/from/session_id 正确，否则业务 ChatMessageService 会判定无会话丢弃。模拟推送请调用 send_push_mock_rule(ruleId)。"
		}
		return jsonText(map[string]any{
			"rule":   pushRuleSummary(rule),
			"hint":   hint,
			"filled": filledFields,
		}), nil
	}
}

// latestRealPacketForIdentity 拉该设备最近一条真实（mocked=false）packet 的 payloadText，
// 用于手写模板身份字段继承。不限 bizType（身份字段是账号/会话维度，不挑业务）。
// 拉不到（无会话/无 packet/全是 fake 包）返回 ok=false，调用方按 best-effort 跳过。
func latestRealPacketForIdentity(ctx context.Context, c *mnpapi.Client, app, did string) (string, bool) {
	q := capture.LongConnectionQuery{
		EventType: capture.LongConnectionEventPacket,
		Limit:     50,
	}
	res, err := c.GetDeviceLongConn(ctx, app, did, q)
	if err != nil || res.SessionID == "" {
		return "", false
	}
	for _, e := range res.Entries {
		if e.Mocked {
			continue
		}
		if strings.TrimSpace(e.PayloadText) != "" {
			return e.PayloadText, true
		}
	}
	return "", false
}

func mcpUpdatePushMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		ruleID := mustString(p, "ruleId")
		newPayload := mustString(p, "replacePayload")
		if ruleID == "" {
			return mcp.NewToolResultError("ruleId 必填（来自 list_push_mock_rules）。"), nil
		}
		if strings.TrimSpace(newPayload) == "" {
			return mcp.NewToolResultError("replacePayload 必填（新的推送 payload 模板）。"), nil
		}

		// Read-modify-write（服务端 PUT 是全量覆盖 + 改内容必带 note）：
		// 保留 match/note/enabled，仅替换 payload；原 base64 二进制模板在传
		// 文本 payload 时显式转为文本模式（避免 SDK 仍按旧 base64 构造）。
		existing, err := c.GetPushMockRule(ctx, app, did, ruleID)
		if err != nil {
			return toolErr(err), nil
		}
		in := &capture.UpdatePushMockRuleInput{
			Match:          existing.Match,
			ReplacePayload: newPayload,
			Note:           &existing.Note,
		}
		if existing.ReplacePayloadBase64 != "" {
			in.ReplacePayloadBase64 = ""
		}
		rule, err := c.UpdatePushMockRule(ctx, app, did, ruleID, in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule":        pushRuleSummary(rule),
			"payloadMode": pushPayloadMode(rule),
			"hint":        "模板 payload 已更新。模拟推送请调用 send_push_mock_rule(ruleId)。",
		}), nil
	}
}

func mcpDeletePushMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		ruleID := mustString(p, "ruleId")
		if err := c.DeletePushMockRule(ctx, app, did, ruleID); err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"deleted": true,
			"ruleId":  ruleID,
			"hint":    "推送模板已删除。已入队的 pending 指令不受影响，SDK 仍会消费。",
		}), nil
	}
}

func mcpSendPushMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		cmd, err := c.SendPushMockRule(ctx, app, did, mustString(p, "ruleId"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"commandId": cmd.ID,
			"ruleId":    cmd.RuleID,
			"channelId": cmd.ChannelID,
			"bizType":   cmd.BizType,
			"hint":      "指令已入队（pending）——入队成功 ≠ 已到达设备。SDK 约 3s 心跳拉取后构造注入；到达证据是稍后长连接日志出现 mocked=true 的 packet。可重复 send 连续下发多条。",
		}), nil
	}
}

func mcpGetLongConnPacketSamples(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		limit := 5
		if v, ok := p["limit"].(float64); ok {
			limit = int(v)
		}
		if limit < 1 {
			limit = 1
		}
		if limit > 20 {
			limit = 20
		}
		// 服务端无 mocked 过滤参数，内部多拉 packet 后在 MCP 层滤掉 fake 包，
		// 保证返回的都是真实骨架（多拉量 = max(limit*3, 50)，上限 500）。
		fetchLimit := limit * 3
		if fetchLimit < 50 {
			fetchLimit = 50
		}
		if fetchLimit > 500 {
			fetchLimit = 500
		}
		q := capture.LongConnectionQuery{
			EventType: capture.LongConnectionEventPacket,
			BizType:   mustString(p, "bizType"),
			Limit:     fetchLimit,
		}
		res, err := c.GetDeviceLongConn(ctx, app, did, q)
		if err != nil {
			return toolErr(err), nil
		}
		if res.SessionID == "" {
			return mcp.NewToolResultText("该设备没有任何长连接日志会话（可能未在抓包或 48h 保留期已过）。请先在真机激活长连接抓包，或确认 app/did 正确。"), nil
		}
		samples := make([]map[string]any, 0, limit)
		for _, e := range res.Entries {
			if e.Mocked {
				continue // 只给真实骨架，模拟推送产生的 fake 包不作参考
			}
			samples = append(samples, map[string]any{
				"seq":         e.Seq,
				"ts":          e.TS,
				"channelId":   e.ChannelID,
				"bizType":     e.BizType,
				"msgId":       e.MsgID,
				"mocked":      e.Mocked,
				"payloadText": e.PayloadText,
			})
			if len(samples) >= limit {
				break
			}
		}
		return jsonText(map[string]any{
			"sessionId": res.SessionID,
			"entries":   samples,
			"total":     len(samples),
			"hint":      "骨架仅供构造模板 payload 参考：msgid/session_id/time 每次下发前必须换成全新随机大整数防判重；touser/fromuser/name 继承当前账号真实会话骨架，不能跨账号抄。",
		}), nil
	}
}

// pushRuleSummary 是列表/创建/更新共用的规则摘要投影（不塞整包 payload）。
func pushRuleSummary(r *capture.PushMockRuleView) map[string]any {
	if r == nil || r.PushMockRule == nil {
		return nil
	}
	return map[string]any{
		"ruleId":    r.ID,
		"channelId": r.ChannelID,
		"bizType":   r.BizType,
		"enabled":   r.Enabled,
		"effective": r.Effective,
		"hitCount":  r.HitCount,
	}
}

// pushPayloadMode 报告模板 payload 的构造形态（SDK 按 base64 优先构造）。
func pushPayloadMode(r *capture.PushMockRuleView) string {
	if r != nil && r.PushMockRule != nil && r.ReplacePayloadBase64 != "" {
		return "binary"
	}
	return "text"
}

// parseLongConnQuery 解析 list_longconn_logs 的过滤参数：校验 eventType 枚举与
// RFC3339 时间窗；limit 默认 100 上限 500；非法值返回带指引的错误结果。
func parseLongConnQuery(p map[string]any) (capture.LongConnectionQuery, *mcp.CallToolResult) {
	var q capture.LongConnectionQuery
	q.ChannelID = mustString(p, "channelId")
	q.BizType = mustString(p, "bizType")
	q.Keyword = mustString(p, "keyword")
	if v, ok := p["eventType"].(string); ok && v != "" {
		switch capture.LongConnectionEventType(v) {
		case capture.LongConnectionEventConnect, capture.LongConnectionEventDisconnect,
			capture.LongConnectionEventHeartbeat, capture.LongConnectionEventPacket,
			capture.LongConnectionEventAck, capture.LongConnectionEventSnapshot:
			q.EventType = capture.LongConnectionEventType(v)
		default:
			return q, mcp.NewToolResultError("eventType 取值无效：" + v + "（合法值 connect|disconnect|heartbeat|packet|ack|snapshot）。")
		}
	}
	if v, ok := p["from"].(string); ok && v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, mcp.NewToolResultError("from 不是合法 RFC3339 时间（如 2026-10-15T10:00:00+08:00）：" + v)
		}
		q.From = &t
	}
	if v, ok := p["to"].(string); ok && v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, mcp.NewToolResultError("to 不是合法 RFC3339 时间（如 2026-10-15T10:00:00+08:00）：" + v)
		}
		q.To = &t
	}
	if v, ok := p["since"].(float64); ok && v > 0 {
		q.Since = int64(v)
	}
	q.Limit = 100
	if v, ok := p["limit"].(float64); ok && v > 0 {
		q.Limit = int(v)
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	if v, ok := p["offset"].(float64); ok && v > 0 {
		q.Offset = int(v)
	}
	return q, nil
}

// ---- list_longconn_logs ------------------------------------------------------

func toolListLongConnLogs() mcp.Tool {
	return mcp.NewTool("list_longconn_logs",
		mcp.WithDescription("查该设备长连接日志（全部事件类型：connect/disconnect/heartbeat/packet/ack/snapshot）。默认摘要投影（id/seq/ts/eventType/channelId/bizType/msgId/mocked/ackSent/duplicate/status/payload 截断预览）；includePayload=true 时每条带完整 payloadText/payloadBase64。支持事件类型/通道/业务/关键字/时间窗/seq 增量（since）过滤；出参附 latestSnapshot（最近一条连接快照，含连接状态）。"+
			"何时用：①查长连接收包；②send_push_mock_rule 之后确认模拟推送结果（过滤/扫描 mocked=true 的新 packet）；③找目标日志后接 create_push_mock_rule_from_log 复现。"+
			"注意：Web 列表只展示 packet；要看业务包传 eventType=packet。模拟推送产生的 fake 包带 mocked=true，可在结果中筛选。错误处理：401 配置 API Key；403 仅会话 owner/admin（显式传跨设备 sessionId 时）；404 会话不存在或 48h 保留期已过。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("sessionId", mcp.Description("会话 ID（可选；缺省自动解析：capturing 优先 → 最近 ended 保留期内）")),
		mcp.WithString("eventType", mcp.Description("事件类型精确过滤：connect|disconnect|heartbeat|packet|ack|snapshot")),
		mcp.WithString("channelId", mcp.Description("按通道标识精确过滤（如 xcpush_tcp）")),
		mcp.WithString("bizType", mcp.Description("按业务类型精确过滤（如 1=IM 聊天）")),
		mcp.WithString("keyword", mcp.Description("payload/字段子串过滤（大小写不敏感）")),
		mcp.WithString("from", mcp.Description("起始时间（RFC3339，含边界）")),
		mcp.WithString("to", mcp.Description("结束时间（RFC3339，含边界）")),
		mcp.WithNumber("since", mcp.Description("seq 增量游标：只返回 seq>since 的日志（增量拉取）")),
		mcp.WithNumber("limit", mcp.Description("返回条数（默认 100，最大 500）")),
		mcp.WithNumber("offset", mcp.Description("分页偏移（先过滤后分页）")),
		mcp.WithBoolean("includePayload", mcp.Description("是否带完整 payload（默认 false；true 时每条含 payloadText/payloadBase64，注意上下文体积，建议配合小 limit）")),
	)
}

func mcpListLongConnLogs(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		q, usageErr := parseLongConnQuery(p)
		if usageErr != nil {
			return usageErr, nil
		}
		var res *mnpapi.LongConnResult
		if sid := mustString(p, "sessionId"); sid != "" {
			res, err = c.ListSessionLongConn(ctx, sid, q)
		} else {
			res, err = c.GetDeviceLongConn(ctx, app, did, q)
		}
		if err != nil {
			return toolErr(err), nil
		}
		if res.SessionID == "" {
			return mcp.NewToolResultText("该设备没有任何长连接日志会话（可能未在抓包或 48h 保留期已过）。请先在真机激活长连接抓包，或确认 app/did 正确。"), nil
		}
		includePayload := false
		if v, ok := p["includePayload"].(bool); ok {
			includePayload = v
		}
		entries := make([]map[string]any, 0, len(res.Entries))
		var latestSnapshot map[string]any
		for _, e := range res.Entries {
			entry := map[string]any{
				"id":             e.ID,
				"seq":            e.Seq,
				"ts":             e.TS,
				"eventType":      e.EventType,
				"channelId":      e.ChannelID,
				"bizType":        e.BizType,
				"msgId":          e.MsgID,
				"mocked":         e.Mocked,
				"mockRuleId":     e.MockRuleID,
				"ackSent":        e.AckSent,
				"duplicate":      e.Duplicate,
				"status":         e.Status,
				"payloadPreview": pushPayloadPreview(e.PayloadText),
			}
			if includePayload {
				entry["payloadText"] = e.PayloadText
				entry["payloadBase64"] = e.PayloadBase64
			}
			entries = append(entries, entry)
			if e.EventType == capture.LongConnectionEventSnapshot {
				// entries 按 seq 升序，最后一次赋值即最新快照。
				latestSnapshot = map[string]any{"seq": e.Seq, "ts": e.TS, "status": e.Status}
			}
		}
		out := map[string]any{
			"sessionId": res.SessionID,
			"entries":   entries,
			"total":     res.Total,
		}
		if latestSnapshot != nil {
			out["latestSnapshot"] = latestSnapshot
		}
		return jsonText(out), nil
	}
}

// ---- create_push_mock_rule_from_log ------------------------------------------

func toolCreatePushMockRuleFromLog() mcp.Tool {
	return mcp.NewTool("create_push_mock_rule_from_log",
		mcp.WithDescription("从一条长连接日志一键创建推送模板：以日志的 channelId/bizType 为模板域、payload 为模板 payload 原样固化，并把原日志快照（source：channelId/bizType/msgId/payloadText/capturedAt）随规则持久化。创建即启用（enabled=true），可直接 send_push_mock_rule。"+
			"何时用：把某条真实 push「复现成假推送」——先 list_longconn_logs 找到目标记录拿 logId，再本工具建模板，再 send。可选 replacePayload 覆盖默认 payload（缺省=原日志 payloadText）。"+
			"⚠️ 默认 payload 与真实包相同（msgid/session_id/time 都是真实值），send 前必须先 update_push_mock_rule 把 msgid/session_id/time 换成全新随机大整数——否则 IM 客户端按 id 判重，假 push 被静默丢弃、UI 无变化；注意协议层 ACK 正常回传，日志里仍会显示 mocked=true+ackSent=true，不要据此误判为送达。touser/fromuser/name 保持当前设备登录账号骨架，勿跨账号抄。"+
			"注意：不做 Web 前端的 [mock] 标记注入——需要业务 UI 显示 mock 标识时请用 replacePayload 自行改 payload 文本。错误处理：404 logId 不在该设备日志中（48h 保留期内）或无会话；403 仅 owner/admin 可创建。模板池语义：同 channelId+bizType 域允许多条模板共存、全部生效——重复建同域模板合法（用 note 区分）。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("sessionId", mcp.Description("会话 ID（可选；缺省自动解析，同 list_longconn_logs）")),
		mcp.WithString("logId", mcp.Required(), mcp.Description("日志记录 ID（来自 list_longconn_logs entries[].id）")),
		mcp.WithString("replacePayload", mcp.Description("覆盖模板 payload（可选；缺省=原日志 payloadText，无文本时用 payloadBase64）")),
		mcp.WithString("note", mcp.Description("规则备注（可选；缺省自动填 'from log <logId>'，避免后续 update 被'改内容必带 note'约束卡住）")),
	)
}

func mcpCreatePushMockRuleFromLog(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		logID := mustString(p, "logId")
		if logID == "" {
			return mcp.NewToolResultError("logId 必填（来自 list_longconn_logs entries[].id）。"), nil
		}

		// 定位记录：sessionId 缺省时自动解析（capturing 优先），然后按 logId
		// 分页扫描（48h 窗口上限 1000 条，扫描预算 3 页 × 500 = 1500，覆盖上限）。
		sessionID := mustString(p, "sessionId")
		if sessionID == "" {
			probe, err := c.GetDeviceLongConn(ctx, app, did, capture.LongConnectionQuery{Limit: 1})
			if err != nil {
				return toolErr(err), nil
			}
			sessionID = probe.SessionID
			if sessionID == "" {
				return mcp.NewToolResultText("该设备没有任何长连接日志会话（可能未在抓包或 48h 保留期已过），无法定位 logId。"), nil
			}
		}
		var rec *capture.LongConnectionRecord
		for offset := 0; offset < 1500 && rec == nil; offset += 500 {
			page, err := c.ListSessionLongConn(ctx, sessionID, capture.LongConnectionQuery{Limit: 500, Offset: offset})
			if err != nil {
				return toolErr(err), nil
			}
			for i := range page.Entries {
				if page.Entries[i].ID == logID {
					rec = &page.Entries[i]
					break
				}
			}
			if len(page.Entries) < 500 {
				break
			}
		}
		if rec == nil {
			return mcp.NewToolResultText("未找到日志记录 logId=" + logID + "（不在该设备/会话的 48h 保留期内）。请重新 list_longconn_logs 确认 logId。"), nil
		}

		// 组装模板（对齐 Web 一键建：channelId/bizType 带出 + source 快照 + enabled）。
		note := mustString(p, "note")
		if strings.TrimSpace(note) == "" {
			// 自动填默认 note：避免后续 update 改 payload 时被服务端"改内容必带 note"约束 400。
			note = "from log " + logID
		}
		in := &capture.PushMockRuleInput{
			ChannelID: rec.ChannelID,
			BizType:   rec.BizType,
			Enabled:   true,
			Note:      note,
			Source: &capture.PushMockSource{
				ChannelID:     rec.ChannelID,
				BizType:       rec.BizType,
				MsgID:         rec.MsgID,
				PayloadText:   rec.PayloadText,
				PayloadBase64: rec.PayloadBase64,
				CapturedAt:    rec.TS,
			},
		}
		if payload := mustString(p, "replacePayload"); payload != "" {
			in.ReplacePayload = payload
		} else if rec.PayloadText != "" {
			// 自动把真实包的 msgid/session_id/time 转占位符，send 时自动轮换防判重。
			in.ReplacePayload = rewriteRealPayloadToTemplate(rec.PayloadText)
		} else {
			in.ReplacePayloadBase64 = rec.PayloadBase64
		}
		if strings.TrimSpace(in.ReplacePayload) == "" && in.ReplacePayloadBase64 == "" {
			return mcp.NewToolResultText("该日志无 payload 文本/base64，无法用作模板；请换一条 packet 日志或用 create_push_mock_rule 手写。"), nil
		}
		rule, err := c.CreatePushMockRule(ctx, app, did, in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule": pushRuleSummary(rule),
			"hint": "已从日志一键建模板并启用（enabled=true）。msgid/session_id/time 已自动转为 ${fake_*} 占位符，send 时自动换新值防判重。模拟推送请调用 send_push_mock_rule(ruleId)。",
		}), nil
	}
}

// ---- get_push_mock_rule -------------------------------------------------------

func toolGetPushMockRule() mcp.Tool {
	return mcp.NewTool("get_push_mock_rule",
		mcp.WithDescription("读取单条推送模板完整配置：channelId/bizType/match/replacePayload（完整文本，不截断）/replacePayloadBase64/enabled/effective/命中统计/note/source 原日志快照/owner。"+
			"何时用：编辑模板前核对当前 payload 与来源日志（source 快照）；确认某 ruleId 的命中次数与生效状态。错误处理：403 非 owner/admin；404 模板不存在（或不属于该设备）。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("模板 ID（来自 list_push_mock_rules）")),
	)
}

func mcpGetPushMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app, did, argErr := requireAppDid(p)
		if argErr != nil {
			return argErr, nil
		}
		r, err := c.GetPushMockRule(ctx, app, did, mustString(p, "ruleId"))
		if err != nil {
			return toolErr(err), nil
		}
		view := map[string]any{
			"ruleId":               r.ID,
			"channelId":            r.ChannelID,
			"bizType":              r.BizType,
			"match":                r.Match,
			"replacePayload":       r.ReplacePayload,
			"replacePayloadBase64": r.ReplacePayloadBase64,
			"enabled":              r.Enabled,
			"effective":            r.Effective,
			"hitCount":             r.HitCount,
			"lastHitAt":            r.LastHitAt,
			"createdAt":            r.CreatedAt,
			"updatedAt":            r.UpdatedAt,
			"note":                 r.Note,
			"owner":                r.Owner,
		}
		if r.Source != nil {
			view["source"] = map[string]any{
				"channelId":     r.Source.ChannelID,
				"bizType":       r.Source.BizType,
				"msgId":         r.Source.MsgID,
				"payloadText":   r.Source.PayloadText,
				"payloadBase64": r.Source.PayloadBase64,
				"capturedAt":    r.Source.CapturedAt,
			}
		}
		return jsonText(map[string]any{"rule": view}), nil
	}
}
