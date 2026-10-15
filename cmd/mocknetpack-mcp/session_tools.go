package main

// MCP capture-session management tools (M12.8): get_capture_status,
// start_capture, renew_capture_observer, release_capture_observer.
//
// These tools only orchestrate the existing admin API session endpoints
// (POST /sessions + the /viewers lease endpoints) — the gateway owns no
// session state. The stateless HTTP gateway derives a deterministic observer
// (viewer) identity from the caller's API key plus an optional workflow-level
// observerId, so reconnecting after a dropped connection or gateway restart
// renews the SAME lease instead of leaking a duplicate one:
//
//	viewerId = "mcp-" + sha256hex(apiKey + "|" + observerId)[:32]
//
// Isolation consequences:
//   - same key + same observerId  => one shared lease (renew is idempotent);
//   - same key + different observerId => independent leases (parallel jobs);
//   - different keys              => leases can never collide;
//   - release only ever deletes an id derived from the caller's OWN key, so
//     one caller can never release another user's observer lease.
//
// Server-side semantics the tool results make explicit:
//   - POST /sessions is idempotent: a capturing session is reused untouched
//     (no traffic cleared, no rules changed, no session ended);
//   - releasing the LAST viewer ends the session and the server then disables
//     every enabled mock rule of the device (rules are kept, not deleted);
//   - ended sessions keep their traffic for 48h (retainUntil).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
)

// Documented server defaults (store.DefaultCaptureConfig). The authenticated
// API does not echo these config values, so the status tool can only report
// the defaults; the authoritative lease TTL always comes from a register /
// renew response (lease.ttlSeconds / lease.expiresAt).
const (
	defaultHeartbeatTimeoutSeconds = 60
	defaultObserverLeaseTTLSeconds = 120
	defaultObserverID              = "default"
)

// observerIDPattern constrains the workflow-level observer id so the derived
// viewer id stays path-safe and the label stays short and predictable.
var observerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// errBadObserverID is returned when the workflow-level observer id contains
// characters outside the safe set or exceeds the length limit.
var errBadObserverID = errors.New("observerId 仅允许字母、数字与 . _ : -（1-64 字符），请修正后重试；不影响其他观察者。")

// observerIdentity is the gateway-derived lease identity of one caller.
type observerIdentity struct {
	observerID string // workflow-level id (default "default")
	viewerID   string // deterministic server-side viewer id
	label      string
}

// deriveObserverIdentity builds the lease identity from the caller's API key
// and the optional workflow-level observerId. The key never appears in the
// viewer id (one-way hash) and is never logged.
func deriveObserverIdentity(apiKey, observerID, label string) (observerIdentity, error) {
	if observerID == "" {
		observerID = defaultObserverID
	}
	if !observerIDPattern.MatchString(observerID) {
		return observerIdentity{}, errBadObserverID
	}
	if label == "" {
		label = "mcp:" + observerID
	} else if utf8.RuneCountInString(label) > 64 {
		return observerIdentity{}, errors.New("label 自定义备注最多 64 字符，请缩短后重试。")
	}
	sum := sha256.Sum256([]byte(apiKey + "|" + observerID))
	viewerID := "mcp-" + hex.EncodeToString(sum[:])[:32]
	return observerIdentity{observerID: observerID, viewerID: viewerID, label: label}, nil
}

// observerFromContext validates arguments and derives the caller identity.
func observerFromContext(ctx context.Context, p map[string]any) (observerIdentity, *mcp.CallToolResult) {
	key, _ := ctx.Value(ctxBearerKey).(string)
	if key == "" {
		return observerIdentity{}, mcp.NewToolResultError("缺少认证：请求未携带有效 API Key，请在 Authorization 头配置 Bearer <你的长效APIKey>（Web 登录 → 账号 → API Keys 创建）")
	}
	observerID := ""
	if value, supplied := p["observerId"]; supplied {
		var ok bool
		observerID, ok = value.(string)
		if !ok {
			return observerIdentity{}, mcp.NewToolResultError("observerId 必须是字符串；缺省或空字符串使用 default。")
		}
	}
	label, _ := p["label"].(string)
	id, err := deriveObserverIdentity(key, observerID, strings.TrimSpace(label))
	if err != nil {
		return observerIdentity{}, mcp.NewToolResultError(err.Error())
	}
	return id, nil
}

// requireAppDid validates the device coordinates shared by every tool.
func requireAppDid(p map[string]any) (string, string, *mcp.CallToolResult) {
	app := strings.TrimSpace(mustString(p, "app"))
	did := strings.TrimSpace(mustString(p, "did"))
	if app == "" || did == "" {
		return "", "", mcp.NewToolResultError("app 与 did 均为必填（应用 bundle id 与设备标识），请先用 list_devices 确认目标设备。")
	}
	return app, did, nil
}

// sessionView is the compact session projection shared by tool results.
func sessionView(s *capture.CaptureSession) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"id":           s.ID,
		"status":       s.Status,
		"startedAt":    s.StartedAt,
		"endedAt":      s.EndedAt,
		"retainUntil":  s.RetainUntil,
		"requestCount": s.RequestCount,
		"viewerCount":  s.ViewerCount,
	}
}

// leaseView renders the authoritative lease timeout information.
func leaseView(id observerIdentity, lease *capture.ViewerLease) map[string]any {
	renewEvery := lease.TTLSeconds / 2
	if renewEvery < 1 {
		renewEvery = 1
	}
	return map[string]any{
		"observerId":                   id.observerID,
		"viewerId":                     id.viewerID,
		"label":                        id.label,
		"ttlSeconds":                   lease.TTLSeconds,
		"expiresAt":                    lease.ExpiresAt,
		"recommendedRenewEverySeconds": renewEvery,
	}
}

// ---- tool definitions (schemas) -------------------------------------------------

func toolGetCaptureStatus() mcp.Tool {
	return mcp.NewTool("get_capture_status",
		mcp.WithDescription("查询某台设备的抓包状态：设备在线状态（idle/capturing/offline）、当前抓包会话 ID 与开始时间/请求数/观察者数、"+
			"最近一个已结束会话及其 48h 保留截止（retainUntil），以及心跳与观察者租约超时信息。"+
			"何时用：开始抓包前的第一步——确认设备在线、是否已有抓包会话可复用；也用于释放租约后核对现场。"+
			"注意：timeouts 中为服务端默认值，你自己持有的租约权威 TTL/expiresAt 以 start_capture、renew_capture_observer 的返回为准。"+
			"错误处理：401 配置 API Key；404 设备未注册或无权访问（先 list_devices）。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
	)
}

func toolStartCapture() mcp.Tool {
	return mcp.NewTool("start_capture",
		mcp.WithDescription("创建或复用抓包会话，并把当前调用者登记为该会话的观察者（拿到观察者租约）。"+
			"幂等且安全：设备已有 capturing 会话时直接复用（created=false），不结束旧会话、不清空已有流量、不改变任何规则；没有会话时才新建（created=true）。"+
			"同一调用者（相同 API Key 与 observerId）重复调用只刷新自己的租约 TTL。"+
			"长任务请按返回的 recommendedRenewEverySeconds（默认 60s，租约 TTL 默认 120s）周期调用 renew_capture_observer；"+
			"结束时调用 release_capture_observer 只释放你自己的租约——当你是最后一个观察者时，释放会结束会话并由服务端批量停用本设备规则（返回中会明确告知）。"+
			"登记成功后若会话刷新失败，仍返回已取得的租约，sessionSnapshotStale=true 与 warning 标明 session 为登记前快照；请 get_capture_status 核对。"+
			"错误处理：404 设备未注册（先 Web/扫码接入）；409 device_offline 设备离线（真机打开 App 恢复心跳后重试）。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("observerId", mcp.Description("工作流级稳定标识（字母数字 . _ : -，1-64 字符），缺省 default；同一 API Key 并行跑多个互不干扰的工作流时用不同值，断线重连务必沿用同一值")),
		mcp.WithString("label", mcp.Description("观察者租约备注（可选，自定义备注≤64 字符；缺省 mcp:<observerId>，最多68字符）")),
	)
}

func toolRenewCaptureObserver() mcp.Tool {
	return mcp.NewTool("renew_capture_observer",
		mcp.WithDescription("续租当前调用者在抓包会话上的观察者租约（同 observerId 重复登记即续租，观察者计数不增加），返回新的 ttlSeconds/expiresAt。"+
			"何时用：抓包/等命中耗时较长时，按 start_capture 返回的 recommendedRenewEverySeconds（默认 60s）周期续租，防止租约 TTL（默认 120s）过期导致会话被服务端结束。"+
			"sessionId 可省略（自动解析该设备当前 capturing 会话）；observerId 必须与 start_capture 时一致。"+
			"错误处理：无抓包会话或会话已结束（409 session_ended）→ 重新 start_capture；404 会话 id 无效。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("sessionId", mcp.Description("会话 ID（可选；缺省自动选取该设备当前 capturing 会话）")),
		mcp.WithString("observerId", mcp.Description("工作流级稳定标识，缺省 default，须与 start_capture 一致")),
	)
}

func toolReleaseCaptureObserver() mcp.Tool {
	return mcp.NewTool("release_capture_observer",
		mcp.WithDescription("释放当前调用者自己的抓包观察者租约（幂等：重复释放、释放不存在的租约都成功），只可能删除自己 Key 派生的观察者，绝不影响其他使用者。"+
			"返回释放前后观察者计数与会话状态：当你是最后一个观察者时，服务端会结束会话并批量停用本设备 Mock 规则（规则不删除、流量保留 48h），此时 sessionEnded=true 并给出恢复提示。rulesDisabledOnDevice 按会话结束的正常语义推断，未核实规则写入，实际启用状态请 list_mock_rules 核对。"+
			"何时用：抓包任务结束、恢复现场时。若不希望自己一离开就结束会话，可设 keepSessionWhenAlone=true（读取的快照中观察者数≤1时跳过释放并返回 released=false）。该检查不是原子操作，其他观察者并发离开时仍可能结束会话；不能保证持续抓包。"+
			"错误处理：401 配置 API Key；404 会话 id 无效。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
		mcp.WithString("sessionId", mcp.Description("会话 ID（可选；缺省自动选取当前 capturing 会话；无 capturing 会话时按幂等成功返回）")),
		mcp.WithString("observerId", mcp.Description("工作流级稳定标识，缺省 default，须与 start_capture 一致")),
		mcp.WithBoolean("keepSessionWhenAlone", mcp.Description("true 时读取的快照 viewerCount≤1 则跳过释放返回 released=false；非原子检查，其他观察者并发离开仍可能结束会话；缺省 false")),
	)
}

// ---- tool handlers ------------------------------------------------------------

func mcpGetCaptureStatus(base string) server.ToolHandlerFunc {
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

		dv, err := c.GetDevice(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}
		sessions, err := c.ListSessions(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}

		var active, latestEnded *capture.CaptureSession
		for _, s := range sessions {
			if s == nil {
				continue
			}
			if s.Status == capture.SessionStatusCapturing && active == nil {
				active = s
			}
			if s.Status == capture.SessionStatusEnded && latestEnded == nil {
				latestEnded = s
			}
		}
		if active == nil && dv.CurrentSession != nil && dv.CurrentSession.Status == capture.SessionStatusCapturing {
			active = dv.CurrentSession
		}

		deviceInfo := map[string]any{
			"app":        dv.App,
			"did":        dv.Did,
			"name":       dv.Name,
			"appName":    dv.AppName,
			"status":     dv.Status,
			"platform":   dv.Platform,
			"osVersion":  dv.OSVersion,
			"sdkVersion": dv.SDKVersion,
			"appVersion": dv.AppVersion,
			"lastSeenAt": dv.LastSeenAt,
		}
		out := map[string]any{
			"device":             deviceInfo,
			"session":            sessionView(active),
			"latestEndedSession": sessionView(latestEnded),
			"timeouts": map[string]any{
				"heartbeatTimeoutSeconds": defaultHeartbeatTimeoutSeconds,
				"observerLeaseTTLSeconds": defaultObserverLeaseTTLSeconds,
				"recommendedRenewSeconds": defaultObserverLeaseTTLSeconds / 2,
				"source":                  "服务端默认值；你持有租约的权威 TTL/expiresAt 以 start_capture、renew_capture_observer 返回为准",
			},
		}
		switch {
		case dv.Status == capture.DeviceStatusOffline:
			out["hint"] = "设备 offline（心跳超时）：请在真机让 App 回到前台或重新打开，状态恢复非 offline 后再 start_capture。"
		case active != nil:
			out["hint"] = "已在抓包中：start_capture 可直接挂载/续租你自己的观察者（复用会话，不影响流量与规则）；查命中用 get_device_traffic(since=<lastSeq>)。"
		default:
			out["hint"] = "设备 idle 待命：调用 start_capture 开始抓包（自动复用或新建会话并登记观察者租约）。"
		}
		return jsonText(out), nil
	}
}

func mcpStartCapture(base string) server.ToolHandlerFunc {
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
		id, idErr := observerFromContext(ctx, p)
		if idErr != nil {
			return idErr, nil
		}

		session, created, err := c.ActivateSession(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}
		lease, err := c.RegisterViewer(ctx, session.ID, id.viewerID, id.label)
		if err != nil {
			// Race: the reused session ended between activate and register.
			// Activating again creates a fresh session, then re-register.
			if ae, ok := err.(*mnpapi.APIError); ok && ae.Status == 409 && ae.Code == "session_ended" {
				session, created, err = c.ActivateSession(ctx, app, did)
				if err != nil {
					return toolErr(err), nil
				}
				lease, err = c.RegisterViewer(ctx, session.ID, id.viewerID, id.label)
				if err != nil {
					return toolErr(err), nil
				}
			} else {
				return toolErr(err), nil
			}
		}
		// The activation snapshot predates our registration; refetch so the
		// returned viewerCount reflects the lease we just (re)attached.
		refreshed, refreshErr := c.GetSession(ctx, session.ID)
		if refreshErr == nil {
			session = refreshed
		}

		out := map[string]any{
			"created":              created,
			"reused":               !created,
			"session":              session,
			"sessionSnapshotStale": refreshErr != nil,
			"observer": map[string]any{
				"observerId": id.observerID,
				"viewerId":   id.viewerID,
				"label":      id.label,
			},
			"lease": leaseView(id, lease),
			"hint":  "已开始/复用抓包并挂载你的观察者租约。长任务按 recommendedRenewEverySeconds 续租；配置规则用 create_mock_rule* 与 set_mock_rule_enabled；结束只调 release_capture_observer（复用不清理流量、不改规则）。",
		}
		if refreshErr != nil {
			// Registration succeeded: retain the authoritative lease and session
			// ID for renewal/cleanup, but disclose the pre-registration snapshot.
			out["warning"] = "观察者租约已登记，但会话刷新失败；session 是登记前快照，viewerCount 等字段可能过期。请用 get_capture_status 核对；续租/释放请携带返回的 session.id 与 observerId。"
			out["hint"] = out["warning"]
		}
		return jsonText(out), nil
	}
}

func mcpRenewCaptureObserver(base string) server.ToolHandlerFunc {
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
		id, idErr := observerFromContext(ctx, p)
		if idErr != nil {
			return idErr, nil
		}

		session, resErr := resolveCapturingSession(ctx, c, app, did, mustString(p, "sessionId"))
		if resErr != nil {
			return resErr, nil
		}
		lease, err := c.RegisterViewer(ctx, session.ID, id.viewerID, id.label)
		if err != nil {
			return toolErr(err), nil
		}
		fresh, err := c.GetSession(ctx, session.ID)
		if err != nil {
			return toolErr(err), nil
		}
		return jsonText(map[string]any{
			"sessionId": session.ID,
			"observer": map[string]any{
				"observerId": id.observerID,
				"viewerId":   id.viewerID,
			},
			"lease":   leaseView(id, lease),
			"session": sessionView(fresh),
			"hint":    "租约已续租。请继续按 recommendedRenewEverySeconds 周期续租；任务结束调用 release_capture_observer。",
		}), nil
	}
}

func mcpReleaseCaptureObserver(base string) server.ToolHandlerFunc {
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
		id, idErr := observerFromContext(ctx, p)
		if idErr != nil {
			return idErr, nil
		}
		explicitSession := strings.TrimSpace(mustString(p, "sessionId"))
		keepWhenAlone, _ := p["keepSessionWhenAlone"].(bool)

		session, err := resolveSessionForRelease(ctx, c, app, did, explicitSession)
		if err != nil {
			return toolErr(err), nil
		}
		if session == nil {
			// No capturing session and no explicit session id: there is no
			// live lease of ours to release — idempotent success.
			return jsonText(map[string]any{
				"released":      true,
				"idempotent":    true,
				"activeSession": false,
				"observer":      map[string]any{"observerId": id.observerID, "viewerId": id.viewerID},
				"hint":          "当前没有抓包中的会话，无观察者租约需要释放（幂等成功）。旧会话流量在 48h 保留期内仍可查。",
			}), nil
		}

		beforeCount := session.ViewerCount
		beforeStatus := session.Status
		if beforeStatus == capture.SessionStatusEnded {
			// EndSession clears the lease map; an ended session has no live
			// observers even if the persisted count field is stale.
			beforeCount = 0
		}
		if keepWhenAlone && beforeStatus == capture.SessionStatusCapturing && beforeCount <= 1 {
			return jsonText(map[string]any{
				"released":  false,
				"reason":    "sole_observer_session_would_end",
				"sessionId": session.ID,
				"session":   sessionView(session),
				"observer":  map[string]any{"observerId": id.observerID, "viewerId": id.viewerID},
				"hint":      "当前快照的观察者数≤1：释放可能结束会话并由服务端批量停用本设备规则。需要保持抓包请继续 renew_capture_observer；确认要结束请不带 keepSessionWhenAlone 重新调用。",
			}), nil
		}

		if err := c.ReleaseViewer(ctx, session.ID, id.viewerID); err != nil {
			return toolErr(err), nil
		}
		after, err := c.GetSession(ctx, session.ID)
		if err != nil {
			return toolErr(err), nil
		}
		afterCount := after.ViewerCount
		if after.Status == capture.SessionStatusEnded {
			// EndSession clears the lease map without rewriting the persisted
			// viewerCount; an ended session has zero live observers by contract.
			afterCount = 0
		}
		sessionEnded := beforeStatus == capture.SessionStatusCapturing && after.Status == capture.SessionStatusEnded
		idempotent := !sessionEnded && afterCount == beforeCount

		out := map[string]any{
			"released":   true,
			"idempotent": idempotent,
			"sessionId":  session.ID,
			"observer": map[string]any{
				"observerId": id.observerID,
				"viewerId":   id.viewerID,
			},
			"before":                map[string]any{"status": beforeStatus, "viewerCount": beforeCount},
			"after":                 map[string]any{"status": after.Status, "viewerCount": afterCount, "retainUntil": after.RetainUntil},
			"sessionEnded":          sessionEnded,
			"rulesDisabledOnDevice": sessionEnded,
		}
		switch {
		case sessionEnded:
			out["traffic"] = map[string]any{"retainedUntil": after.RetainUntil, "retentionHours": 48}
			out["hint"] = "释放前后观测到会话已结束（与 Web 关闭页面同语义）：服务端按既有逻辑停用本设备 Mock 规则（未删除），rulesDisabledOnDevice 为该语义的推断，实际状态请 list_mock_rules 核对；流量保留 48h 可查；下次 start_capture 后如需 Mock 请重新 set_mock_rule_enabled。"
		case idempotent:
			out["hint"] = "你的租约此前已不存在（释放幂等成功），会话与其他观察者不受影响。"
		default:
			out["hint"] = "已释放你自己的观察者租约，会话仍在抓包，其他观察者不受影响。"
		}
		return jsonText(out), nil
	}
}

// resolveCapturingSession resolves the target session for register/renew:
// an explicit sessionId is fetched and passed through (an ended session
// surfaces the server's 409 on register); without one, the device's current
// capturing session is required.
func resolveCapturingSession(ctx context.Context, c *mnpapi.Client, app, did, explicitSessionID string) (*capture.CaptureSession, *mcp.CallToolResult) {
	if explicitSessionID != "" {
		s, err := c.GetSession(ctx, explicitSessionID)
		if err != nil {
			return nil, toolErr(err)
		}
		return s, nil
	}
	s, err := c.CurrentCapturingSession(ctx, app, did)
	if err != nil {
		return nil, toolErr(err)
	}
	if s == nil {
		return nil, mcp.NewToolResultError("该设备当前没有抓包中的会话：请先调用 start_capture 开始/复用抓包；已结束会话的流量在 48h 保留期内仍可用 get_device_traffic 查询。")
	}
	return s, nil
}

// resolveSessionForRelease resolves the session for a release. An explicit
// id is always honored (releasing against an ended session is a server-side
// no-op). Without one, only a capturing session is relevant; nil, nil means
// "nothing live to release".
func resolveSessionForRelease(ctx context.Context, c *mnpapi.Client, app, did, explicitSessionID string) (*capture.CaptureSession, error) {
	if explicitSessionID != "" {
		return c.GetSession(ctx, explicitSessionID)
	}
	return c.CurrentCapturingSession(ctx, app, did)
}

// ---- list_sessions -----------------------------------------------------------

func toolListSessions() mcp.Tool {
	return mcp.NewTool("list_sessions",
		mcp.WithDescription("列该设备抓包会话历史（capturing 优先；ended 会话 48h 保留期内可查，服务端已过滤过期会话），返回 sessionId/status/startedAt/endedAt/retainUntil/requestCount/viewerCount。"+
			"何时用：①查历史会话的 sessionId——配合 list_longconn_logs / get_device_traffic 的 sessionId 参数，读取 ended 会话保留期内的长连接日志与流量；②start_capture 之前确认当前抓包状态。"+
			"注意：sessionId 参数缺省时工具按「capturing 优先 → 最近 ended」自动解析；要查历史会话必须先本工具拿 sessionId 再显式传入。错误处理：403 仅 owner/admin 可见。"),
		mcp.WithString("app", mcp.Required(), mcp.Description("应用标识（bundle id）")),
		mcp.WithString("did", mcp.Required(), mcp.Description("设备标识")),
	)
}

func mcpListSessions(base string) server.ToolHandlerFunc {
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
		sessions, err := c.ListSessions(ctx, app, did)
		if err != nil {
			return toolErr(err), nil
		}
		views := make([]map[string]any, 0, len(sessions))
		for _, s := range sessions {
			if s == nil {
				continue
			}
			views = append(views, map[string]any{
				"sessionId":    s.ID,
				"status":       s.Status,
				"startedAt":    s.StartedAt,
				"endedAt":      s.EndedAt,
				"retainUntil":  s.RetainUntil,
				"requestCount": s.RequestCount,
				"viewerCount":  s.ViewerCount,
			})
		}
		out := map[string]any{"sessions": views, "total": len(views)}
		if len(views) == 0 {
			out["hint"] = "该设备没有任何可见会话（未抓包，或 ended 会话已过 48h 保留期）。"
		}
		return jsonText(out), nil
	}
}
