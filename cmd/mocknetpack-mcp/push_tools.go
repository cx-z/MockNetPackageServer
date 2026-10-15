package main

// push mock MCP tools (能力线 L7 模拟推送形态): list_push_mock_rules,
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
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/capture"
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
		mcp.WithDescription("凭空创建推送模板：channelId 必填 + replacePayload 必填（payload 模板快照，SDK 构造假 Packet 时写入 bizData）。创建即启用（enabled=true），可直接 send_push_mock_rule。"+
			"何时用：模拟一条未抓包过的推送。"+
			"重要——payload 不要凭字段名从零编：先调 get_longconn_packet_samples 拉同 bizType 的真实日志作骨架，只改内层展示文本；"+
			"msgid/session_id/time 每次下发前换成全新随机大整数（否则客户端按 id 判重，假 push 被静默丢弃）；touser/fromuser/name 必须继承当前设备登录账号的真实会话骨架，不能跨账号抄。错误处理：400 channelId 缺失；403 仅 owner/admin。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("channelId", mcp.Required(), mcp.Description("通道标识（必填，如 xcpush_tcp）")),
		mcp.WithString("bizType", mcp.Description("业务类型（可选，如 1=IM 聊天；空=不限定）")),
		mcp.WithString("replacePayload", mcp.Required(), mcp.Description("推送 payload 模板（必填；建议来自 get_longconn_packet_samples 的真实骨架，仅改内层展示文本）")),
	)
}

func toolUpdatePushMockRule() mcp.Tool {
	return mcp.NewTool("update_push_mock_rule",
		mcp.WithDescription("编辑推送模板的 replacePayload（模板 payload；channelId/bizType 域不可改）。read-modify-write：只传新 payload 即可，note/其他字段自动保留；"+
			"若原模板是 base64 二进制 payload，传 replacePayload 后自动转为文本模式。"+
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
			"何时用：让业务收到一条 mock push。错误处理：409 push_rule_disabled 模板停用（先 update_push_mock_rule 恢复）；403 仅规则 owner/admin；404 模板不存在。"),
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
			if channelFilter != "" && r.ChannelId != channelFilter {
				continue
			}
			payload := r.ReplacePayload
			if payload == "" {
				payload = r.ReplacePayloadBase64
			}
			views = append(views, map[string]any{
				"ruleId":         r.ID,
				"channelId":      r.ChannelId,
				"bizType":        r.BizType,
				"payloadPreview": pushPayloadPreview(payload),
				"enabled":        r.Enabled,
				"effective":      r.Effective,
				"hitCount":       r.HitCount,
				"note":           r.Note,
			})
		}
		out := map[string]any{"version": list.Version, "rules": views, "total": len(views)}
		if len(list.Conflicts) > 0 {
			out["conflicts"] = list.Conflicts
			out["hint"] = "检测到同 channelId+bizType 域多条启用模板（conflicts）：该域规则不生效，SDK 不注入；请先停用/删除冲突模板。"
		}
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
			ChannelId:      mustString(p, "channelId"),
			BizType:        mustString(p, "bizType"),
			ReplacePayload: mustString(p, "replacePayload"),
			Enabled:        true, // 一键建模板语义：创建即启用，可直接 send
		}
		if in.ChannelId == "" {
			return mcp.NewToolResultError("channelId 必填（通道标识，如 xcpush_tcp）。"), nil
		}
		if strings.TrimSpace(in.ReplacePayload) == "" {
			return mcp.NewToolResultError("replacePayload 必填（推送 payload 模板）。建议先用 get_longconn_packet_samples 拉真实骨架再改内层文本。"), nil
		}
		rule, err := c.CreatePushMockRule(ctx, app, did, in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule": pushRuleSummary(rule),
			"hint": "推送模板已创建且启用（enabled=true）。模拟推送请调用 send_push_mock_rule(ruleId)。",
		}), nil
	}
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
			Note:           existing.Note,
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
			"channelId": cmd.ChannelId,
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
		"channelId": r.ChannelId,
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
