package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: rule_registry.go
// Mock rules: CRUD, Effective/conflict evaluation, version, retention janitor,
// disable-on-session-end (pure move from capture_registry.go).
// ============================================================================
// Mock rules
// ============================================================================

// interfaceKey identifies a mockable endpoint by Method + URL path (Query/Body
// do not participate in matching, requirement ).
type interfaceKey struct {
	method string
	path   string
}

// evaluateRules recomputes the runtime Effective flag for every rule of a device
// and collects the abnormal multi-enabled interfaces. A rule is Effective only
// when it is the sole enabled rule on its interface; an interface with >1
// enabled rule mocks nothing and is reported as a conflict.
func (m *CaptureManager) evaluateRules(ctx context.Context, app, did string) ([]*capture.MockRuleView, []capture.MockRuleConflict, int, error) {
	all, err := m.rules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		return nil, nil, 0, err
	}
	version, err := m.rules.GetRuleVersion(ctx, app, did)
	if err != nil {
		return nil, nil, 0, err
	}

	enabledCount := make(map[interfaceKey]int)
	for _, r := range all {
		if r.Enabled {
			enabledCount[interfaceKey{r.Method, r.Path}]++
		}
	}

	views := make([]*capture.MockRuleView, 0, len(all))
	for _, r := range all {
		eff := r.Enabled && enabledCount[interfaceKey{r.Method, r.Path}] == 1
		c := *r
		views = append(views, &capture.MockRuleView{MockRule: &c, Effective: eff})
	}

	var conflicts []capture.MockRuleConflict
	for k, n := range enabledCount {
		if n > 1 {
			conflicts = append(conflicts, capture.MockRuleConflict{
				Method:  k.method,
				Path:    k.path,
				Message: MockRuleConflictMessage,
			})
		}
	}
	return views, conflicts, version, nil
}

// CreateMockRule persists a new rule. When enabled=true it enforces the
// single-active rule per interface: if another enabled rule already matches the
// same Method+Path it returns ErrRuleConflict (409). The conflict check, the
// insert and the rule-set version bump run in ONE store lock (4.9) — no reader
// can observe the rule without its version. caller stamps the rule's Owner
// (: creator username, server-side field); nil caller (--no-auth) leaves
// Owner empty, which  treats as a legacy rule (admin-manageable only).
// Every developer may create rules; ownership is established by this stamp.
//
//	Step1 : a hand-authored rule (Source == nil, created from the Web
//
// blank form rather than captured traffic) must carry a non-blank note — with
// no source snapshot the note is the only semantic anchor ("what does this rule
// test?"). Capture-originated creates (Source != nil) are exempt. The guard
// runs lock-side before the Mutate so a rejected create bumps no version and
// leaves no partial row.
func (m *CaptureManager) CreateMockRule(ctx context.Context, app, did string, in *capture.MockRuleInput, caller *RuleCaller) (*capture.MockRuleView, int, error) {
	if in.Source == nil && strings.TrimSpace(in.Note) == "" {
		return nil, 0, ErrNoteRequired
	}

	// 方案 D：从视频流量一键创建（"Mock 此请求"带 OriginTrafficID）时，初始
	// 回包为 302 → 原 URL（未编辑即跳转、不存字节）。规则一旦被编辑（PUT 置
	// Edited=true）就脱离该快路径，回包由用户内容决定。非视频流量仍记录
	// OriginTrafficID 作来源溯源，但回包原样保留。
	//
	// M15：大图流量（responseStaticKind=image，字节已在入库时丢弃）走回源：
	// 服务端按原请求头代表抓包设备取一次原图存成 blob，规则只带 MediaRef，
	// SDK 收到后按 ref 拉 blob。设备 SDK 版本不支持 media 时回落 302 → 原 URL
	// （与视频快路径同结构），绝不把「只有占位文本」的规则当成可用规则下发。
	var (
		videoRedirect *capture.MockResponse
		mediaPlan     *mediaBuildPlan
	)
	if in.OriginTrafficID != "" {
		// 跨设备权限隔离：先校验 OriginTrafficID 属于当前 (app, did)，
		// 防止跨设备引用其他设备的视频 URL 作为 302 Location。
		originApp, originDid, err := m.GetTrafficWithOwner(ctx, in.OriginTrafficID)
		if err != nil {
			return nil, 0, err
		}
		if originApp != app || originDid != did {
			return nil, 0, fmt.Errorf("origin traffic %s does not belong to device %s/%s", in.OriginTrafficID, app, did)
		}
		origin, err := m.GetTraffic(ctx, in.OriginTrafficID)
		if err != nil {
			return nil, 0, err
		}
		if origin.ResponseStaticKind == capture.StaticVideo && origin.URL != "" {
			videoRedirect = &capture.MockResponse{
				StatusCode: http.StatusFound,
				Headers:    map[string]string{"Location": origin.URL},
			}
		} else if origin.ResponseStaticKind == capture.StaticImage {
			dev, err := m.devices.Get(ctx, app, did)
			if err != nil {
				return nil, 0, err
			}
			if supportsMockMedia(dev.SDKVersion) {
				mediaPlan = &mediaBuildPlan{
					url:            origin.URL,
					contentType:    capture.ResponseContentType(origin.ResponseHeaders),
					statusCode:     mediaReplayStatusCode(origin.StatusCode),
					requestHeaders: origin.RequestHeaders,
				}
			} else if origin.URL != "" {
				// M15 老 SDK 回落（与视频快路径同结构）：旧版本没有 media 拉取能力，
				// 规则字节又已丢弃，只能给 302 → 原 URL 让设备自己去源站取。源站对象
				// 可能已过期，这是「至少能试一次」的降级，不是新字节来源。
				videoRedirect = &capture.MockResponse{
					StatusCode: http.StatusFound,
					Headers:    map[string]string{"Location": origin.URL},
				}
			}
		}
	}

	// 回源取数在 Mutate 之前完成：网络等待绝不能占住存储锁（Mutate 内是同步
	// 回调，持锁期间做 IO 会阻塞全部规则读写）。取数成功才进入落库阶段；落库
	// 失败则删掉刚写的 blob，保证「没有指向空 blob 的规则」。
	var (
		createdID string
		mediaRef  *capture.MediaRef
	)
	if mediaPlan != nil {
		if m.mediaStore == nil {
			return nil, 0, ErrMediaUnavailable
		}
		createdID = id.ULID()
		data, contentType, err := FetchMedia(ctx, mediaPlan.url, mediaPlan.requestHeaders)
		if err != nil {
			return nil, 0, err
		}
		// 回源请求头是抓包设备那份白名单，可能让 CDN 回 application/octet-stream
		//（Accept 不含 image 时会发生），而抓包当时同一 URL 明确是 image/jpeg。
		// 回放类型优先用抓包记录的类型，只在抓包也没有时接受回源的通用类型。
		if (contentType == "" || contentType == "application/octet-stream") && mediaPlan.contentType != "" {
			contentType = mediaPlan.contentType
		}
		ref, err := m.mediaStore.Put(createdID, data, contentType)
		if err != nil {
			if errors.Is(err, ErrMediaTooLarge) {
				return nil, 0, &MediaFetchError{Code: MediaReasonTooLarge, Message: err.Error()}
			}
			if errors.Is(err, ErrMediaQuotaExceeded) {
				return nil, 0, &MediaFetchError{Code: MediaReasonStorageFull, Message: err.Error()}
			}
			return nil, 0, err
		}
		mediaRef = ref
	}

	now := time.Now()
	var created *capture.MockRule
	version, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		if in.Enabled {
			for _, r := range rules {
				if r.Enabled && r.Method == in.Method && r.Path == in.Path {
					return nil, false, ErrRuleConflict
				}
			}
		}
		var owner string
		if caller != nil {
			owner = caller.Username
		}
		ruleID := createdID
		if ruleID == "" {
			ruleID = id.ULID()
		}
		created = &capture.MockRule{
			ID:              ruleID,
			App:             app,
			Did:             did,
			Method:          in.Method,
			Path:            in.Path,
			Response:        in.Response,
			Enabled:         in.Enabled,
			Note:            in.Note,
			Source:          in.Source,
			OriginTrafficID: in.OriginTrafficID,
			CreatedAt:       now,
			UpdatedAt:       now,
			LastUsedAt:      now,
			Owner:           owner,
			UpdatedBy:       owner,
		}
		if videoRedirect != nil {
			created.Response = *videoRedirect
		}
		if mediaRef != nil {
			// media 分支整体替换回包（headers 换成回源 Content-Type 是契约要求），
			// 但延迟/状态码属于用户输入，不能跟着一起丢：编辑时设的 delayMs 必须
			// 在 media 规则上继续生效，否则「加 800ms 延迟」会静默失效。
			created.Response = capture.MockResponse{
				StatusCode: mediaPlan.statusCode,
				Headers:    map[string]string{"Content-Type": mediaRef.ContentType},
				DelayMs:    in.Response.DelayMs,
				Media:      mediaRef,
			}
		}
		return append(rules, created), true, nil
	})
	if err != nil {
		if mediaRef != nil {
			m.mediaStore.Delete(createdID)
		}
		return nil, 0, err
	}
	// 规则已在上面落库并推进版本：此处再因派生视图计算失败而返回 error，会让
	// 客户端以为创建失败（重试还会撞 ErrRuleConflict），实际规则已存在。派生视图
	// 只用于展示 effective/conflict，失败时回落到入参，不否定已提交的写入；
	// 但存储读故障仍要留日志，否则被吞掉的错误无从排查。
	views, _, _, evalErr := m.evaluateRules(ctx, app, did)
	if evalErr == nil {
		for _, v := range views {
			if v.ID == created.ID {
				return v, version, nil
			}
		}
	} else {
		m.log.Warn("create rule: evaluate derived view failed, falling back to input response",
			"app", app, "did", did, "rule", created.ID, "error", evalErr)
	}
	return &capture.MockRuleView{MockRule: created, Effective: in.Enabled}, version, nil
}

// canManageOwner reports whether caller may manage a rule owned by owner (/).
// nil caller (--no-auth smoke mode) always passes, mirroring the requireAuth
// bypass; admins pass; otherwise only the owner passes. An empty owner is a
// legacy rule (pre-) — admin-manageable only . Shared by HTTP mock rules
// (canManageRule) and push mock rules (push_rule_registry.go).
func canManageOwner(caller *RuleCaller, owner string) bool {
	if caller == nil {
		return true
	}
	if caller.IsAdmin {
		return true
	}
	return owner != "" && owner == caller.Username
}

// canManageRule reports whether caller may mutate rule r (/).
func canManageRule(caller *RuleCaller, r *capture.MockRule) bool {
	return canManageOwner(caller, r.Owner)
}

// UpdateMockRule edits a rule's canned response, note, and/or enabled switch
// . The match key (Method+Path) and the source snapshot are immutable —
// the input type UpdateMockRuleInput deliberately omits them. Turning the switch
// on is rejected with ErrRuleConflict if another enabled rule already matches
// the rule's (frozen) interface. An absent Enabled pointer leaves the current
// switch untouched. The update and the version bump run in ONE store lock (4.9).
// : only the rule's owner or an admin may edit/toggle; any other caller
// gets ErrRuleForbidden. For non-admin callers a missing ruleID also returns
// ErrRuleForbidden (not ErrRuleNotFound) so rule existence cannot be probed.
func (m *CaptureManager) UpdateMockRule(ctx context.Context, app, did, ruleID string, in *capture.UpdateMockRuleInput, caller *RuleCaller) (*capture.MockRuleView, int, error) {
	var updated *capture.MockRule
	// M15：编辑把规则从 media 路径改回普通回包（新 Response 不带 Media）时，
	// 落库成功后删除旧 blob——否则文件会一直占着配额直到重启做 orphan 清理。
	var mediaClearedID string
	version, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		var existing *capture.MockRule
		for _, r := range rules {
			if r.ID == ruleID {
				existing = r
				break
			}
		}
		if existing == nil {
			// 不泄露规则存在性：非 admin 对未知 ruleID 统一按无权限处理。
			if caller != nil && !caller.IsAdmin {
				return nil, false, ErrRuleForbidden
			}
			return nil, false, ErrRuleNotFound
		}
		//  权限矩阵：编辑/启停仅 owner 与 admin。
		if !canManageRule(caller, existing) {
			return nil, false, ErrRuleForbidden
		}

		// : note is required only when the PUT actually edits the canned
		// response (statusCode/headers/body changed). A pure toggle echoes the
		// stored response unchanged and may leave the note blank — rules created
		// from a capture ("Mock 此请求") carry no note and enabling them must not
		// force an edit.
		if !reflect.DeepEqual(existing.Response, in.Response) && strings.TrimSpace(in.Note) == "" {
			return nil, false, ErrNoteRequired
		}

		// Enforce single-active on the frozen interface when the edit turns the
		// rule on (Enabled pointer present and true, while currently off).
		if in.Enabled != nil && *in.Enabled && !existing.Enabled {
			for _, r := range rules {
				if r.ID != ruleID && r.Enabled && r.Method == existing.Method && r.Path == existing.Path {
					return nil, false, ErrRuleConflict
				}
			}
		}

		c := *existing
		if existing.Response.Media != nil && in.Response.Media == nil {
			mediaClearedID = existing.ID
		}
		c.Response = in.Response
		c.Note = in.Note
		// 方案 D：任何一次保存（含仅开关切换）都视为已编辑，使该规则脱离
		// "未编辑视频规则 → 302 跳转"快路径；此后回包由用户内容决定
		// （测空 body / 500 / 损坏字节等边界 case）。
		c.Edited = true
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		c.UpdatedAt = time.Now()
		// Editing a rule or toggling it counts as "used" ( sliding window).
		c.LastUsedAt = c.UpdatedAt
		// : stamp the last modifier from the current session user.
		if caller != nil {
			c.UpdatedBy = caller.Username
		}
		updated = &c

		out := make([]*capture.MockRule, 0, len(rules))
		for _, r := range rules {
			if r.ID == ruleID {
				out = append(out, updated)
			} else {
				out = append(out, r)
			}
		}
		return out, true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	if mediaClearedID != "" {
		m.mediaStore.Delete(mediaClearedID)
	}
	views, _, _, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, version, err
	}
	for _, v := range views {
		if v.ID == ruleID {
			return v, version, nil
		}
	}
	return nil, version, ErrRuleNotFound
}

// DeleteMockRule removes a rule by ID (scoped to app/did). The delete and the
// version bump run in ONE store lock (4.9) so the SDK always sees the rule-set
// version advance together with the removal. : only the rule's owner or an
// admin may delete; other callers get ErrRuleForbidden (and, like Update, a
// missing ruleID is masked as ErrRuleForbidden for non-admin callers).
func (m *CaptureManager) DeleteMockRule(ctx context.Context, app, did, ruleID string, caller *RuleCaller) (int, error) {
	var deletedID string
	version, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		for i, r := range rules {
			if r.ID == ruleID {
				if !canManageRule(caller, r) {
					return nil, false, ErrRuleForbidden
				}
				deletedID = r.ID
				out := make([]*capture.MockRule, 0, len(rules)-1)
				out = append(out, rules[:i]...)
				out = append(out, rules[i+1:]...)
				return out, true, nil
			}
		}
		if caller != nil && !caller.IsAdmin {
			return nil, false, ErrRuleForbidden
		}
		return nil, false, ErrRuleNotFound
	})
	if err == nil && deletedID != "" {
		m.mediaStore.Delete(deletedID)
	}
	return version, err
}

// ListMockRules returns every rule of the device (Web view, including disabled)
// with the runtime Effective flag, the abnormal conflicts, and the current
// rule-set version.
func (m *CaptureManager) ListMockRules(ctx context.Context, app, did string) ([]*capture.MockRuleView, []capture.MockRuleConflict, int, error) {
	return m.evaluateRules(ctx, app, did)
}

// ListActiveMockRules is the SDK pull. When the device rule-set version equals
// sinceVersion nothing changed: it returns no rules and changed=false. Otherwise
// it returns only the Effective rules (abnormal interfaces are excluded
// entirely) and the new version.
func (m *CaptureManager) ListActiveMockRules(ctx context.Context, app, did string, sinceVersion int) ([]*capture.MockRuleView, int, bool, error) {
	views, _, version, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, 0, false, err
	}
	if version == sinceVersion {
		return nil, version, false, nil
	}
	active := make([]*capture.MockRuleView, 0, len(views))
	for _, v := range views {
		if v.Effective {
			active = append(active, v)
		}
	}
	return active, version, true, nil
}

// RuleVersion returns the current monotonic rule-set version for (app, did).
// Used by the device heartbeat to tell the SDK whether it should pull an
// updated rule snapshot.
func (m *CaptureManager) RuleVersion(ctx context.Context, app, did string) (int, error) {
	return m.rules.GetRuleVersion(ctx, app, did)
}

// ============================================================================
// Rule retention janitor
// ============================================================================

// ruleUsageTime picks the sliding-window baseline for a rule: LastUsedAt,
// falling back to UpdatedAt (and then CreatedAt) for legacy rows that predate
//
//	and have no LastUsedAt persisted yet.
func ruleUsageTime(r *capture.MockRule) time.Time {
	if !r.LastUsedAt.IsZero() {
		return r.LastUsedAt
	}
	if !r.UpdatedAt.IsZero() {
		return r.UpdatedAt
	}
	return r.CreatedAt
}

// PurgeExpiredRules deletes every mock rule whose last use is older than the
// configured retention (default 7d, sliding window). A deleted rule bumps its
// device rule-set version so the SDK drops it from its local snapshot on the
// next heartbeat. Best-effort: per-rule errors are logged, not fatal.
func (m *CaptureManager) PurgeExpiredRules(ctx context.Context) {
	if m.cfg.MockRuleRetention <= 0 {
		return
	}
	all, err := m.rules.List(ctx, nil)
	if err != nil {
		m.log.Warn("rule janitor: list rules failed", "error", err)
		return
	}
	now := time.Now()
	bumped := make(map[string]bool) // (app\0did) already bumped
	for _, r := range all {
		if now.Sub(ruleUsageTime(r)) <= m.cfg.MockRuleRetention {
			continue
		}
		if err := m.rules.Delete(ctx, r.ID); err != nil {
			m.log.Warn("rule janitor: delete expired rule failed", "rule", r.ID, "error", err)
			continue
		}
		m.mediaStore.Delete(r.ID)
		key := r.App + "\x00" + r.Did
		if !bumped[key] {
			if _, err := m.rules.BumpRuleVersion(ctx, r.App, r.Did); err != nil {
				m.log.Warn("rule janitor: bump version failed", "app", r.App, "did", r.Did, "error", err)
			}
			bumped[key] = true
		}
		m.log.Info("rule janitor: purged expired mock rule",
			"rule", r.ID, "method", r.Method, "path", r.Path, "lastUsedAt", ruleUsageTime(r))
	}
}

// disableDeviceRules flips every enabled mock rule of a device off when its
// capture session ends . Rules are NOT deleted — they remain
// in the Web rule history — but they are no longer effective; on the next
// capture session the user must re-enable each one manually. The whole disable
// set and the version bump run in ONE store lock (4.9) so the SDK drops them
// atomically.
func (m *CaptureManager) disableDeviceRules(ctx context.Context, app, did string) {
	if _, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		changed := false
		for i, r := range rules {
			if !r.Enabled {
				continue
			}
			c := *r
			c.Enabled = false
			rules[i] = &c
			changed = true
		}
		if !changed {
			return rules, false, nil
		}
		return rules, true, nil
	}); err != nil {
		m.log.Warn("session end: disable rules failed", "app", app, "did", did, "error", err)
	}
}

// ============================================================================
// Mock rule media (M15 大图字节治理)
// ============================================================================

// mediaBuildPlan carries what must be fetched from the origin before a rule
// row is written: the original URL, the captured Content-Type (fallback when
// the origin response omits it), the replay status code, and the captured
// request headers (credential whitelist for the fetch).
type mediaBuildPlan struct {
	url            string
	contentType    string
	statusCode     int
	requestHeaders map[string][]string
}

// mediaReplayStatusCode picks the status code a media rule replays: a 2xx
// origin status is kept (200/206 for images); anything else (0 for legacy rows,
// 3xx/4xx) falls back to 200 so the SDK does not replay an unusable status
// against a body it does not control.
func mediaReplayStatusCode(status int) int {
	if status >= 200 && status < 300 {
		return status
	}
	return http.StatusOK
}

// GetRuleMedia loads the blob a media rule points at. It is device-scoped:
// the rule must belong to (app, did) and carry a MediaRef, and the bytes must
// still hash to the stored SHA256 (a truncated/replaced file is treated as
// missing rather than served). Missing anything → ErrNotFound.
func (m *CaptureManager) GetRuleMedia(ctx context.Context, app, did, ruleID string) ([]byte, *capture.MediaRef, error) {
	rule, err := m.rules.Get(ctx, ruleID)
	if err != nil {
		return nil, nil, err
	}
	if rule.App != app || rule.Did != did || rule.Response.Media == nil || m.mediaStore == nil {
		return nil, nil, ErrNotFound
	}
	data, err := m.mediaStore.Get(ruleID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	if !verifyMediaHash(data, rule.Response.Media) {
		m.log.Warn("media: blob hash mismatch, refusing to serve", "rule", ruleID)
		return nil, nil, ErrNotFound
	}
	return data, rule.Response.Media, nil
}

// SweepOrphanMedia deletes blobs no rule points at. Called once at startup:
// a crash between Put and Mutate, or rules deleted while the process was down,
// leave files that would otherwise occupy quota forever.
func (m *CaptureManager) SweepOrphanMedia(ctx context.Context) {
	if m.mediaStore == nil {
		return
	}
	all, err := m.rules.List(ctx, nil)
	if err != nil {
		m.log.Warn("media: sweep orphans list rules failed", "error", err)
		return
	}
	keep := make(map[string]bool, len(all))
	for _, r := range all {
		if r.Response.Media != nil {
			keep[r.ID] = true
		}
	}
	m.mediaStore.SweepOrphans(keep)
}
