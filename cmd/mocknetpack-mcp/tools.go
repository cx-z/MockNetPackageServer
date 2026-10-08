package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
)

// ctxBearerKey carries the caller's API key, injected by the HTTP gateway
// (WithHTTPContextFunc) from the request's Authorization header. Tool handlers
// build their admin client from THIS key, so key validity and owner/admin
// permissions are enforced per-account by the mockd admin API — not by a
// gateway-level whitelist.
type ctxKey string

const ctxBearerKey ctxKey = "mocknetpack.bearer.key"

// clientFromContext builds an admin API client from the caller's bearer key in
// ctx. The returned error follows the "给 AI 的指引" convention.
func clientFromContext(ctx context.Context, base string) (*mnpapi.Client, error) {
	key, _ := ctx.Value(ctxBearerKey).(string)
	if key == "" {
		return nil, errors.New("缺少认证：请求未携带有效 API Key，请在 Authorization 头配置 Bearer <你的长效APIKey>（Web 登录 → 账号 → API Keys 创建）")
	}
	return mnpapi.NewClient(base, key), nil
}

// keyFromAuthorization extracts the credential from "Bearer <key>", or "" if
// the header is missing/malformed. Never logs the key itself.
func keyFromAuthorization(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return ""
	}
	return auth[len(prefix):]
}

// ---- tool definitions (schemas) -------------------------------------------------

func toolListDevices() mcp.Tool {
	return mcp.NewTool("list_devices",
		mcp.WithDescription("列出全部已注册设备及其状态（idle 待命 / capturing 抓包中 / offline 离线）。"+
			"何时用：任务的第一步——确认目标设备存在、在抓包中（有活动会话）再查流量。无参数。"))
}

func toolGetDeviceTraffic() mcp.Tool {
	return mcp.NewTool("get_device_traffic",
		mcp.WithDescription("查某台设备的最近请求日志（默认 compact 投影：id/timestamp/method/url/path/statusCode/durationMs/mocked；"+
			"用 since 增量拉取上次之后的新条目，用 method/scheme/keyword/statusCode 过滤）。"+
			"实现为两步编排：取该设备最近会话（capturing 优先）→ 查其流量。"+
			"何时用：查流量/验证 Mock 命中（mocked=true 表示命中规则）。"+
			"错误处理：401 配置 API Key；404 说明 id/资源无效。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("method", mcp.Description("HTTP 方法过滤（GET/POST/PUT/DELETE 等，大小写不敏感）")),
		mcp.WithString("scheme", mcp.Description("URL scheme 过滤（http/https/自定义，按 URL 前缀匹配）")),
		mcp.WithString("keyword", mcp.Description("URL 子串过滤（大小写不敏感）")),
		mcp.WithNumber("statusCode", mcp.Description("响应状态码精确过滤")),
		mcp.WithString("from", mcp.Description("起始时间 RFC3339（含）")),
		mcp.WithString("to", mcp.Description("截止时间 RFC3339（含）")),
		mcp.WithNumber("since", mcp.Description("增量游标：只返回 seq 大于该值的条目（上次拉取的最大 seq；首拉不传）")),
		mcp.WithNumber("limit", mcp.Description("分页大小，默认 100，最大 500")),
	)
}

func toolGetTraffic() mcp.Tool {
	return mcp.NewTool("get_traffic",
		mcp.WithDescription("按 trafficId 取单条请求日志的完整详情（含请求/响应头与 body）。"+
			"何时用：构造 Mock 规则前的数据源查看，或确认某条请求的回包内容。"),
		mcp.WithString("trafficId", mcp.Required(), mcp.Description("流量条目 ID（来自 get_device_traffic 的 entries[].id）")),
	)
}

func toolCreateMockRuleFromTraffic() mcp.Tool {
	return mcp.NewTool("create_mock_rule_from_traffic",
		mcp.WithDescription("从一条真实请求日志创建 Mock 规则（一键创建）：以该请求的 method/path 为匹配键、真实回包为模板，并把原始请求快照（source）随规则持久化。"+
			"何时用：让某接口按真实回包 mock。注意：note 必填且不能为空白；创建后规则默认停用，需 set_mock_rule_enabled 启用。"+
			"错误处理：409 说明同接口已有启用规则（先停用旧的）。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("trafficId", mcp.Required(), mcp.Description("流量条目 ID")),
		mcp.WithString("note", mcp.Required(), mcp.Description("规则备注（必填、非空白；建议写明用途）")),
	)
}

func toolCreateMockRule() mcp.Tool {
	return mcp.NewTool("create_mock_rule",
		mcp.WithDescription("凭空创建一条 Mock 规则（手写回包）：method+path 为匹配键（不含 Query/Body），返回自定义回包。"+
			"何时用：需要模拟一个未抓包过的接口（如异常返回、固定数据）。note 必填且非空白；创建后默认停用，需 set_mock_rule_enabled 启用。"+
			"错误处理：409 同接口已有启用规则。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("method", mcp.Required(), mcp.Description("HTTP 方法（匹配键）")),
		mcp.WithString("path", mcp.Required(), mcp.Description("URL 路径（匹配键，不含 query）")),
		mcp.WithNumber("statusCode", mcp.Description("Mock 回包状态码（默认 200）")),
		mcp.WithObject("headers", mcp.Description("Mock 回包响应头，JSON 对象（字符串值）")),
		mcp.WithString("body", mcp.Description("Mock 回包体（UTF-8 文本）")),
		mcp.WithString("note", mcp.Required(), mcp.Description("规则备注（必填、非空白）")),
	)
}

func toolCreateShare() mcp.Tool {
	return mcp.NewTool("create_share",
		mcp.WithDescription("把一条请求日志生成为分享链接（7 天有效、免登录可读的快照）。"+
			"何时用：把某条请求/回包发给别人查看。"),
		mcp.WithString("trafficId", mcp.Required(), mcp.Description("流量条目 ID")),
	)
}

func toolGetShare() mcp.Tool {
	return mcp.NewTool("get_share",
		mcp.WithDescription("按 shareId 读取分享快照（含原始请求与回包）。何时用：查看分享链接对应的请求详情。"+
			"错误处理：404 说明分享不存在或已过期（7 天）。"),
		mcp.WithString("shareId", mcp.Required(), mcp.Description("分享 ID（create_share 返回的 shareId）")),
	)
}

func toolSetMockRuleEnabled() mcp.Tool {
	return mcp.NewTool("set_mock_rule_enabled",
		mcp.WithDescription("启用或停用一条 Mock 规则。启用后该接口（Method+Path）走 Mock 回包，命中流量 mocked=true；停用后直连（fail-open）。"+
			"错误处理：409 互斥冲突——同接口已有其他启用规则（先停用旧的）；403 仅 owner/admin 可操作。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("规则 ID")),
		mcp.WithBoolean("enabled", mcp.Required(), mcp.Description("true=启用，false=停用")),
	)
}

func toolUpdateMockRule() mcp.Tool {
	return mcp.NewTool("update_mock_rule",
		mcp.WithDescription("编辑一条 Mock 规则的回包（statusCode/headers/body）或备注。匹配键 method/path 不可改。"+
			"实现为：读现有规则 → 合并修改字段 → 覆盖式更新（只传要改的字段即可）。"+
			"二进制规则改文本：设 clearBodyBase64=true（清掉原始二进制字节，body 将以文本回放）；结果含 responseMode=text|binary 标注最终形态。"+
			"编辑回包内容时 note 必填（服务端校验，空/纯空白 400）；纯启停请用 set_mock_rule_enabled。"+
			"错误处理：403 仅 owner/admin 可编辑；404 规则不存在。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("规则 ID")),
		mcp.WithNumber("statusCode", mcp.Description("新的 Mock 回包状态码")),
		mcp.WithObject("headers", mcp.Description("新的响应头（JSON 对象）")),
		mcp.WithString("body", mcp.Description("新的回包体（UTF-8 文本；配合 clearBodyBase64=true 可把二进制快照改为文本 JSON）")),
		mcp.WithBoolean("clearBodyBase64", mcp.Description("true=显式清除原有二进制回包体（bodyBase64），规则改为以 body 文本回放；结果 responseMode 将变为 text")),
		mcp.WithString("note", mcp.Description("规则备注（改动回包内容时必填、非空白）")),
	)
}

func toolListMockRules() mcp.Tool {
	return mcp.NewTool("list_mock_rules",
		mcp.WithDescription("列出设备全部 Mock 规则（ruleId/method/path/enabled/effective/note），支持按 path 子串筛选。"+
			"何时用：任务里需要知道现有规则（如启用前确认、找 ruleId、检查重复）——不必转 Web。"+
			"错误处理：403 仅 owner/admin 可见。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("path", mcp.Description("按 URL 路径子串筛选（大小写不敏感）；不传返回全部")),
	)
}

func toolGetMockRule() mcp.Tool {
	return mcp.NewTool("get_mock_rule",
		mcp.WithDescription("读取单条 Mock 规则完整配置：匹配键 method/path、完整 response（statusCode/headers/body/bodyBase64 是否二进制）、note、enabled/effective。"+
			"何时用：编辑前核对现有回包内容，或确认某 ruleId 的具体匹配与回放形态。"+
			"错误处理：403 非 owner/admin；404 规则不存在或不属于该设备。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("规则 ID（来自 list_mock_rules）")),
	)
}

func toolDeleteMockRule() mcp.Tool {
	return mcp.NewTool("delete_mock_rule",
		mcp.WithDescription("删除一条 Mock 规则（立即生效：该接口不再被 mock）。清理临时规则时使用；如需保留记录请先 set_mock_rule_enabled(enabled=false) 再删。"+
			"错误处理：403 仅 owner/admin 可删；404 规则不存在。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("ruleId", mcp.Required(), mcp.Description("规则 ID（来自 list_mock_rules）")),
	)
}

// ---- tool handlers ------------------------------------------------------------

// jsonText renders any value as the tool's result payload.
func jsonText(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("序列化结果失败: " + err.Error())
	}
	return mcp.NewToolResultText(string(b))
}

// toolErr renders an mnpapi error (or a usage error) as a tool error result
// carrying the "给 AI 的指引" text.
func toolErr(err error) *mcp.CallToolResult {
	if ae, ok := err.(*mnpapi.APIError); ok {
		return mcp.NewToolResultError(ae.Message)
	}
	return mcp.NewToolResultError(err.Error())
}

func mcpListDevices(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		devices, err := c.ListDevices(ctx)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{"devices": devices, "total": len(devices)}), nil
	}
}

func mcpGetDeviceTraffic(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app := mustString(p, "app")
		did := mustString(p, "did")
		q := mnpapi.TrafficQuery{Compact: true}
		if v, ok := p["method"].(string); ok && v != "" {
			q.Method = v
		}
		if v, ok := p["scheme"].(string); ok && v != "" {
			q.Scheme = v
		}
		if v, ok := p["keyword"].(string); ok && v != "" {
			q.Keyword = v
		}
		if v, ok := p["statusCode"].(float64); ok {
			n := int(v)
			q.Status = &n
		}
		if v, ok := p["from"].(string); ok && v != "" {
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return mcp.NewToolResultError("from 必须是 RFC3339 时间戳"), nil
			}
			q.From = &ts
		}
		if v, ok := p["to"].(string); ok && v != "" {
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return mcp.NewToolResultError("to 必须是 RFC3339 时间戳"), nil
			}
			q.To = &ts
		}
		if v, ok := p["since"].(float64); ok {
			q.Since = int64(v)
		}
		if v, ok := p["limit"].(float64); ok {
			q.Limit = int(v)
		}
		res, err := c.GetDeviceTraffic(ctx, app, did, q)
		if err != nil {
			return toolErr(err), nil
		}
		if res.SessionID == "" {
			return mcp.NewToolResultText("该设备没有任何抓包会话（可能未在抓包中）。请先在 Web/真机上激活会话，或确认 app/did 正确。"), nil
		}
		return jsonText(map[string]any{"sessionId": res.SessionID, "entries": res.Entries, "total": res.Total}), nil
	}
}

func mcpGetTraffic(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		e, err := c.GetTraffic(ctx, mustString(req.GetArguments(), "trafficId"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(e), nil
	}
}

func mcpCreateMockRuleFromTraffic(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		rule, err := c.CreateMockRuleFromTraffic(ctx,
			mustString(p, "app"), mustString(p, "did"), mustString(p, "trafficId"), mustString(p, "note"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule":      rule,
			"enabled":   rule.Enabled,
			"effective": rule.Effective,
			"hint":      "规则已创建且默认停用。命中该接口请调用 set_mock_rule_enabled(enabled=true)。",
		}), nil
	}
}

func mcpCreateMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		resp := capture.MockResponse{StatusCode: 200}
		if v, ok := p["statusCode"].(float64); ok {
			resp.StatusCode = int(v)
		}
		if v, ok := p["headers"].(map[string]any); ok && len(v) > 0 {
			h := make(map[string]string, len(v))
			for k, vv := range v {
				switch t := vv.(type) {
				case string:
					h[k] = t
				default:
					h[k] = jsonNumberOrString(vv)
				}
			}
			resp.Headers = h
		}
		if v, ok := p["body"].(string); ok {
			resp.Body = v
		}
		in := &capture.MockRuleInput{
			Method:   mustString(p, "method"),
			Path:     mustString(p, "path"),
			Response: resp,
			Note:     mustString(p, "note"),
		}
		rule, err := c.CreateMockRule(ctx, mustString(p, "app"), mustString(p, "did"), in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule":      rule,
			"enabled":   rule.Enabled,
			"effective": rule.Effective,
			"hint":      "规则已创建且默认停用。命中该接口请调用 set_mock_rule_enabled(enabled=true)。",
		}), nil
	}
}

func mcpCreateShare(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		s, err := c.CreateShare(ctx, mustString(req.GetArguments(), "trafficId"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"shareId":   s.ShareID,
			"url":       s.URL,
			"expiresAt": s.ExpiresAt,
			"hint":      "分享 7 天有效、免登录可读；查看请调用 get_share。",
		}), nil
	}
}

func mcpGetShare(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		s, err := c.GetShare(ctx, mustString(req.GetArguments(), "shareId"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(s), nil
	}
}

func mcpSetMockRuleEnabled(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app := mustString(p, "app")
		did := mustString(p, "did")
		ruleID := mustString(p, "ruleId")
		on, _ := p["enabled"].(bool)

		// The server PUT is a full replace of the canned response (statusCode
		// 100–599 validated, / semantics): a pure toggle must re-send the
		// complete response plus the existing note — read-modify-write, same
		// as CLI `rule set-enabled` and update_mock_rule.
		existing, err := c.GetMockRule(ctx, app, did, ruleID)
		if err != nil {
			return toolErr(err), nil
		}
		in := &capture.UpdateMockRuleInput{
			Response: existing.Response,
			Enabled:  &on,
			Note:     existing.Note,
		}
		rule, err := c.UpdateMockRule(ctx, app, did, ruleID, in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{"rule": rule, "enabled": rule.Enabled, "effective": rule.Effective}), nil
	}
}

func mcpUpdateMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app := mustString(p, "app")
		did := mustString(p, "did")
		ruleID := mustString(p, "ruleId")

		// Read-modify-write: only the provided fields change; the server's
		// update is a full overwrite of response.
		existing, err := c.GetMockRule(ctx, app, did, ruleID)
		if err != nil {
			return toolErr(err), nil
		}
		resp := existing.Response
		if v, ok := p["statusCode"].(float64); ok {
			resp.StatusCode = int(v)
		}
		if v, ok := p["headers"].(map[string]any); ok {
			h := make(map[string]string, len(v))
			for k, vv := range v {
				h[k] = jsonNumberOrString(vv)
			}
			resp.Headers = h
		}
		if v, ok := p["body"].(string); ok {
			resp.Body = v
		}
		// clearBodyBase64=true: drop the binary snapshot so the rule replays the
		// text body (binary → text JSON migration,  P0-3).
		if v, ok := p["clearBodyBase64"].(bool); ok && v {
			resp.BodyBase64 = ""
		}
		note, _ := p["note"].(string)
		in := &capture.UpdateMockRuleInput{Response: resp, Note: note}
		rule, err := c.UpdateMockRule(ctx, app, did, ruleID, in)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule":         rule,
			"enabled":      rule.Enabled,
			"effective":    rule.Effective,
			"responseMode": responseMode(rule.Response),
		}), nil
	}
}

// responseMode reports how the SDK will replay the canned body: binary raw
// bytes (bodyBase64) or UTF-8 text (body).
func responseMode(resp capture.MockResponse) string {
	if resp.BodyBase64 != "" {
		return "binary"
	}
	return "text"
}

func mcpListMockRules(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app := mustString(p, "app")
		did := mustString(p, "did")
		pathFilter := ""
		if v, ok := p["path"].(string); ok {
			pathFilter = strings.ToLower(v)
		}
		rules, err := c.ListMockRules(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}
		views := make([]map[string]any, 0, len(rules))
		for _, r := range rules {
			if pathFilter != "" && !strings.Contains(strings.ToLower(r.Path), pathFilter) {
				continue
			}
			views = append(views, map[string]any{
				"ruleId":    r.ID,
				"method":    r.Method,
				"path":      r.Path,
				"enabled":   r.Enabled,
				"effective": r.Effective,
				"note":      r.Note,
			})
		}
		return jsonText(map[string]any{"rules": views, "total": len(views)}), nil
	}
}

func mcpGetMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		rule, err := c.GetMockRule(ctx, mustString(p, "app"), mustString(p, "did"), mustString(p, "ruleId"))
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"rule":         rule,
			"responseMode": responseMode(rule.Response),
		}), nil
	}
}

func mcpDeleteMockRule(base string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		c, err := clientFromContext(ctx, base)
		if err != nil {
			return toolErr(err), nil
		}
		p := req.GetArguments()
		app := mustString(p, "app")
		did := mustString(p, "did")
		ruleID := mustString(p, "ruleId")
		if err := c.DeleteMockRule(ctx, app, did, ruleID); err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"deleted": true,
			"ruleId":  ruleID,
			"hint":    "规则已删除，该接口不再被 mock。临时规则请用 delete_mock_rule 清理。",
		}), nil
	}
}

// mustString reads a required string argument (empty when missing).
func mustString(p map[string]any, k string) string {
	if v, ok := p[k].(string); ok {
		return v
	}
	return ""
}

func jsonNumberOrString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
