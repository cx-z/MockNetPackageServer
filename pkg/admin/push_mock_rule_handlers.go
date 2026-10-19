// Package admin: push mock rule handlers.
// Web CRUD/toggle + SDK incremental snapshot pull. Routes live under
// /api/v1/devices/{app}/{did}/push-mock-rules — 与 HTTP mock-rules 并列的
// 独立资源（独立模型/独立版本号/独立端点，HTTP 契约零改动）。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// PushMockRuleListResponse is the Web full-list contract PushMockRuleList:
// the device's push rule set plus the current monotonic version. Conflicts is
// only populated for the Web full list (omitted on the SDK incremental pull).
type PushMockRuleListResponse struct {
	Version   int                            `json:"version"`
	Rules     []*capture.PushMockRuleView    `json:"rules"`
	Conflicts []capture.PushMockRuleConflict `json:"conflicts,omitempty"`
}

// sdkPushRuleView is the SDK incremental-pull wire format: only the fields the
// SDK consumes for local matching and replacement. owner/updatedBy/hitCount/
// lastHitAt/source/note are pure server-side/Web-only fields and must never
// reach the SDK (对齐 HTTP sdkRuleView 语义)。
type sdkPushRuleView struct {
	ID                   string                 `json:"id"`
	ChannelID            string                 `json:"channelId"`
	// BizType 恒输出（含空串）：空 bizType 省略字段会让 SDK 非 Optional 模型
	// decode 失败（规则列表整体拉取失败）；SDK 端已对齐 Optional（yaml 非
	// required），空串 = 不限定 bizType。
	BizType              string                 `json:"bizType"`
	Match                capture.PushMockMatch  `json:"match"`
	ReplacePayload       string                 `json:"replacePayload,omitempty"`
	ReplacePayloadBase64 string                 `json:"replacePayloadBase64,omitempty"`
	Enabled              bool                   `json:"enabled"`
	Effective            bool                   `json:"effective"`
}

// toSDKPushRules maps full views to the SDK wire format.
func toSDKPushRules(views []*capture.PushMockRuleView) []sdkPushRuleView {
	out := make([]sdkPushRuleView, 0, len(views))
	for _, v := range views {
		out = append(out, sdkPushRuleView{
			ID:                   v.ID,
			ChannelID:            v.ChannelID,
			BizType:              v.BizType,
			Match:                v.Match,
			ReplacePayload:       v.ReplacePayload,
			ReplacePayloadBase64: v.ReplacePayloadBase64,
			Enabled:              v.Enabled,
			Effective:            v.Effective,
		})
	}
	return out
}

// sdkPushRuleListResponse is the SDK pull response shape: version plus the
// stripped push rule set. Distinct from PushMockRuleListResponse so the wire
// format for the SDK can never accidentally grow Web-only fields.
type sdkPushRuleListResponse struct {
	Version int              `json:"version"`
	Rules   []sdkPushRuleView `json:"rules"`
}

// handleListPushMockRules handles GET /api/v1/devices/{app}/{did}/push-mock-rules.
//
// Two consumers share one route (契约 PushMockRuleList):
//   - SDK incremental pull: ?sinceVersion=N returns only Effective rules. When
//     the device version is unchanged the list is empty (SDK keeps its snapshot).
//   - Web full list: no sinceVersion returns every rule (including disabled,
//     with source snapshots); conflicts is a compatibility field, always empty
//     (模板池语义：多条同域 enabled 模板合法，全部 Effective).
func (a *API) handleListPushMockRules(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")

	raw := r.URL.Query().Get("sinceVersion")
	if raw != "" {
		// SDK incremental pull: no Web Bearer auth (对齐 HTTP mock-rules 分支
		// 模式); SDK identity is (app, did), unknown-did rejection lands in
		// ListActivePushMockRules → evaluatePushRules → store.
		since, err := strconv.Atoi(raw)
		if err != nil || since < 0 {
			writeError(w, http.StatusBadRequest, "invalid_field", "sinceVersion must be a non-negative integer")
			return
		}
		active, version, _, err := a.captureManager.ListActivePushMockRules(r.Context(), app, did, since)
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		sdkRules := toSDKPushRules(active)
		if sdkRules == nil {
			sdkRules = []sdkPushRuleView{}
		}
		writeJSON(w, http.StatusOK, sdkPushRuleListResponse{
			Version: version,
			Rules:   sdkRules,
		})
		return
	}

	// Web full list: require a logged-in Bearer token (route is open for the SDK
	// incremental pull, so auth is enforced here, per-branch; 对齐 HTTP 分支).
	u := a.authenticate(r)
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
	} else if a.apiKeyConfig.Enabled {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}

	views, conflicts, version, err := a.captureManager.ListPushMockRules(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if views == nil {
		views = []*capture.PushMockRuleView{}
	}
	writeJSON(w, http.StatusOK, PushMockRuleListResponse{
		Version:   version,
		Rules:     views,
		Conflicts: conflicts,
	})
}

// handleCreatePushMockRule handles POST /api/v1/devices/{app}/{did}/push-mock-rules.
// Creates a push rule (default disabled). 模板池语义：同域
// （channelId+bizType）允许多条 enabled 规则共存，创建不做域单活跃 409 检查。
func (a *API) handleCreatePushMockRule(w http.ResponseWriter, r *http.Request) {
	var in capture.PushMockRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	// payload 别名 fallback：用户写 "payload" 时自动当 replacePayload 用。
	// 优先级：显式 replacePayload 优先；两者同时提供时 replacePayload 生效，
	// payload 被静默忽略（canonical field wins）。
	if in.ReplacePayload == "" && in.PayloadAlias != "" {
		in.ReplacePayload = in.PayloadAlias
	}
	if !validatePushMockRuleInput(w, &in) {
		return
	}
	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	view, _, err := a.captureManager.CreatePushMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), &in, ruleCaller(r))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// handleUpdatePushMockRule handles PUT /api/v1/devices/{app}/{did}/push-mock-rules/{ruleId}.
// Edits the payload match, replace payload and note; optionally flips the enabled
// switch. channelId/bizType 指针字段可迁移匹配键域；
// 模板池语义：迁移域/开启开关不再返回域单活跃 409（多条同域
// enabled 共存合法）。note must be non-blank only when the PUT actually changes
// rule content (including a match-key domain migration); a pure toggle may leave
// it blank.
func (a *API) handleUpdatePushMockRule(w http.ResponseWriter, r *http.Request) {
	var in capture.UpdatePushMockRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	// payload 别名 fallback：同 create，显式 replacePayload 优先。
	if in.ReplacePayload == "" && in.PayloadAlias != "" {
		in.ReplacePayload = in.PayloadAlias
	}
	if !validateUpdatePushMockRuleInput(w, &in) {
		return
	}
	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	view, _, err := a.captureManager.UpdatePushMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId"), &in, ruleCaller(r))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDeletePushMockRule handles DELETE /api/v1/devices/{app}/{did}/push-mock-rules/{ruleId}.
func (a *API) handleDeletePushMockRule(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	if _, err := a.captureManager.DeletePushMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId"), ruleCaller(r)); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validatePushMockRuleInput enforces the contract required fields
// (PushMockRuleInput: channelId). match 校验：jsonPath 存在时 jsonPathOp 必须
// 是 equals|contains（空按 contains）；jsonPath 为空但 jsonPathValue 非空 →
// 400（值无路径无意义，防止配置漂移）。
func validatePushMockRuleInput(w http.ResponseWriter, in *capture.PushMockRuleInput) bool {
	if in.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "channelId is required")
		return false
	}
	return validatePushMockMatch(w, &in.Match)
}

// validateUpdatePushMockRuleInput validates the edit payload (UpdatePushMockRuleInput).
// channelId 显式提供（编辑迁移域）时不能为空。
func validateUpdatePushMockRuleInput(w http.ResponseWriter, in *capture.UpdatePushMockRuleInput) bool {
	if in.ChannelID != nil && strings.TrimSpace(*in.ChannelID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "channelId cannot be empty")
		return false
	}
	return validatePushMockMatch(w, &in.Match)
}

// validatePushMockMatch enforces the push match condition shape.
func validatePushMockMatch(w http.ResponseWriter, m *capture.PushMockMatch) bool {
	if m.JSONPath == "" && m.JSONPathValue != "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "jsonPathValue requires jsonPath (e.g. $.data.tip)")
		return false
	}
	if m.JSONPath != "" && m.JSONPathOp != "" &&
		m.JSONPathOp != capture.PushMatchOpEquals && m.JSONPathOp != capture.PushMatchOpContains {
		writeError(w, http.StatusBadRequest, "invalid_field", "jsonPathOp must be equals|contains")
		return false
	}
	return true
}

// handleSendPushMockRule handles POST /api/v1/devices/{app}/{did}/push-mock-rules/{ruleId}/send
// Web 点「模拟推送」→ 服务端为设备入队一条
// pending 指令（快照规则 payload）→ SDK 下次心跳携带 → 构造注入。
// requireAuth + authorizeDeviceAccess；规则必须存在且 enabled（停用 409）。
// 同一规则重复触发每次追加新指令（支持连续模拟推送多条）。
func (a *API) handleSendPushMockRule(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")
	ruleID := r.PathValue("ruleId")

	u := a.authenticate(r)
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
	} else if a.apiKeyConfig.Enabled {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	if !canManagePushRule(w, r, a, app, did, ruleID) {
		return
	}

	cmd, err := a.captureManager.EnqueuePushRuleCommand(r.Context(), app, did, ruleID)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSDKPushCommand(*cmd))
}

// toSDKPushCommand maps a pending command to the SDK wire format
// (PushRuleCommandView: 构造 Packet 所需字段，剥离消费标记)。
func toSDKPushCommand(c capture.PushRuleCommand) capture.PushRuleCommandView {
	return capture.PushRuleCommandView{
		ID:            c.ID,
		RuleID:        c.RuleID,
		ChannelID:     c.ChannelID,
		BizType:       c.BizType,
		Payload:       c.Payload,
		PayloadBase64: c.PayloadBase64,
	}
}

// canManagePushRule authorizes a Web rule mutation against the rule owner
// (owner or admin). Non-admin callers on unknown ruleIDs get 403 (existence
// masked, 对齐 HTTP 规则权限语义)。
func canManagePushRule(w http.ResponseWriter, r *http.Request, a *API, app, did, ruleID string) bool {
	views, _, _, err := a.captureManager.ListPushMockRules(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return false
	}
	caller := ruleCaller(r)
	for _, v := range views {
		if v.ID == ruleID {
			if caller == nil {
				return true // --no-auth 冒烟模式全放行
			}
			if caller.IsAdmin {
				return true
			}
			if v.Owner != "" && v.Owner == caller.Username {
				return true
			}
			writeError(w, http.StatusForbidden, "forbidden", "only the rule owner or an admin can send this rule")
			return false
		}
	}
	if caller != nil && !caller.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "rule not found")
		return false
	}
	writeError(w, http.StatusNotFound, "not_found", "push mock rule not found")
	return false
}

// handleListPushRuleCommands handles GET /api/v1/devices/{app}/{did}/push-rule-commands
// (Web: pending 指令状态查询，requireAuth；返回待下发指令的 id/ruleId/createdAt，
// 不含 payload——Web 仅展示状态，敏感内容不下发到浏览器)。
type pushRuleCommandListResponse struct {
	Commands []pushRuleCommandStatusView `json:"commands"`
}

type pushRuleCommandStatusView struct {
	ID        string    `json:"id"`
	RuleID    string    `json:"ruleId"`
	CreatedAt time.Time `json:"createdAt"`
}

func (a *API) handleListPushRuleCommands(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")

	u := a.authenticate(r)
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
	} else if a.apiKeyConfig.Enabled {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}

	pending, err := a.captureManager.ListPendingPushRuleCommands(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	views := make([]pushRuleCommandStatusView, 0, len(pending))
	for _, c := range pending {
		views = append(views, pushRuleCommandStatusView{ID: c.ID, RuleID: c.RuleID, CreatedAt: c.CreatedAt})
	}
	writeJSON(w, http.StatusOK, pushRuleCommandListResponse{Commands: views})
}

// pushRuleCommandAckRequest is the SDK ack body (模拟推送形态)。
type pushRuleCommandAckRequest struct {
	IDs []string `json:"ids"`
}

// handleAckPushRuleCommands handles POST /api/v1/devices/{app}/{did}/push-rule-commands/ack
// (SDK 构造注入完成后回报；open 无鉴权——SDK identity = (app, did)，对齐 SDK
// 增量拉取分支；未知指令 id 幂等忽略)。
func (a *API) handleAckPushRuleCommands(w http.ResponseWriter, r *http.Request) {
	var in pushRuleCommandAckRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if err := a.captureManager.MarkPushRuleCommandsConsumed(r.Context(), r.PathValue("app"), r.PathValue("did"), in.IDs); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
