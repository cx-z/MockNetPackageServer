package store

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: push_rule_registry.go
// Push mock rules (能力线 L7): CRUD, Effective evaluation, version,
// hit statistics, retention janitor. 复用 HTTP MockRule 的管理语义（权限/启停/
// effective/增量拉取），独立模型与独立版本号（L7 决策 D3）。
// 2026-10-16（用户拍板）：push mock 模板池允许多条同域（channelId+bizType）
// enabled 模板共存——Effective 即 enabled，无域冲突报告。
// ============================================================================

// evaluatePushRules computes the runtime Effective flag for every push rule of a
// device. Effective 即 enabled（模板池语义：多条同域共存全部生效；模拟推送按
// 规则 ID 快照成指令下发，无域内匹配歧义——2026-10-16 用户拍板）。
func (m *CaptureManager) evaluatePushRules(ctx context.Context, app, did string) ([]*capture.PushMockRuleView, []capture.PushMockRuleConflict, int, error) {
	if m.pushRules == nil {
		return nil, nil, 0, nil
	}
	all, err := m.pushRules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		return nil, nil, 0, err
	}
	version, err := m.pushRules.GetRuleVersion(ctx, app, did)
	if err != nil {
		return nil, nil, 0, err
	}

	// 2026-10-16（用户拍板）：push mock 模板池允许多条同域（channelId+bizType）
	// enabled 模板共存——模拟推送按规则 ID 快照成指令下发，无域内匹配歧义。
	// Effective 即 enabled（不再要求域内唯一）；不再报告 conflicts。
	views := make([]*capture.PushMockRuleView, 0, len(all))
	for _, r := range all {
		c := *r
		views = append(views, &capture.PushMockRuleView{PushMockRule: &c, Effective: r.Enabled})
	}
	return views, nil, version, nil
}

// SetPushMockRules wires the persistent push rule store (L7 决策 D3：注入模式
// 对齐 SetLongConnStore，NewCaptureManager 签名不变)。nil store 表示 push mock
// 未启用——所有 push 规则方法返回空集（旧测试/未注入环境零影响）。
func (m *CaptureManager) SetPushMockRules(ps PushMockRuleStore) {
	m.pushRules = ps
}

// SetPushRuleCommands wires the persistent pending push command store
// (L7 模拟推送形态 D3：独立于规则集的临时队列)。nil store 表示未启用。
func (m *CaptureManager) SetPushRuleCommands(cs PushRuleCommandStore) {
	m.pushRuleCommands = cs
}

// EnqueuePushRuleCommand 为设备入队一条「模拟推送」指令（Web 点「模拟推送」
// 触发）。规则必须存在且 enabled；指令快照规则当前的 payload（文本/base64），
// 保证下推内容与触发时一致。同一规则重复触发每次追加（支持连续模拟推送多条）。
// 权限由 handler 层校验（canManageOwner）。
func (m *CaptureManager) EnqueuePushRuleCommand(ctx context.Context, app, did, ruleID string) (*capture.PushRuleCommand, error) {
	if m.pushRules == nil || m.pushRuleCommands == nil {
		return nil, ErrRuleNotFound // 未启用时不可见
	}
	all, err := m.pushRules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		return nil, err
	}
	var rule *capture.PushMockRule
	for _, r := range all {
		if r.ID == ruleID {
			rule = r
			break
		}
	}
	if rule == nil {
		return nil, ErrRuleNotFound
	}
	if !rule.Enabled {
		return nil, ErrPushRuleDisabled
	}
	// 空 payload 模板拒绝入队：空指令在 SDK 侧 makePacket 失败且永不 ack，
	// 会卡成永久「待下发」（修复 2026-10-15：PUT 整体覆盖曾把模板 payload 清空）。
	if rule.ReplacePayload == "" && rule.ReplacePayloadBase64 == "" {
		return nil, ErrPushRuleEmptyPayload
	}
	cmd := &capture.PushRuleCommand{
		ID:            id.ULID(),
		RuleID:        rule.ID,
		ChannelId:     rule.ChannelId,
		BizType:       rule.BizType,
		Payload:       rule.ReplacePayload,
		PayloadBase64: rule.ReplacePayloadBase64,
		CreatedAt:     time.Now(),
	}
	if err := m.pushRuleCommands.Append(ctx, app, did, cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// ListPendingPushRuleCommands 返回设备全部未消费指令（SDK 心跳拉取）。未注入
// 指令 store 时返回空集（旧环境零影响）。
func (m *CaptureManager) ListPendingPushRuleCommands(ctx context.Context, app, did string) ([]*capture.PushRuleCommand, error) {
	if m.pushRuleCommands == nil {
		return nil, nil
	}
	return m.pushRuleCommands.ListPending(ctx, app, did)
}

// MarkPushRuleCommandsConsumed 将指令标记为已消费（SDK 构造注入完成后 ack 回报）。
func (m *CaptureManager) MarkPushRuleCommandsConsumed(ctx context.Context, app, did string, ids []string) error {
	if m.pushRuleCommands == nil {
		return nil
	}
	return m.pushRuleCommands.MarkConsumed(ctx, app, did, ids)
}

// CreatePushMockRule persists a new push mock rule. 模板池语义（2026-10-16 用户
// 拍板）：同域（channelId+bizType）允许多条 enabled 规则共存，创建不做域单活跃
// 409 冲突检查——模拟推送按规则 ID 快照成指令下发，无域内匹配歧义。The insert
// and the push rule-set version bump run in ONE store lock. caller stamps the
// rule's Owner (creator username); nil caller (--no-auth) leaves Owner empty
// (legacy rule, admin-manageable only). A hand-authored rule (Source == nil) must
// carry a non-blank note (对齐 HTTP MockRule：无 source 快照时 note 是唯一语义锚点)。
func (m *CaptureManager) CreatePushMockRule(ctx context.Context, app, did string, in *capture.PushMockRuleInput, caller *RuleCaller) (*capture.PushMockRuleView, int, error) {
	if m.pushRules == nil {
		return nil, 0, ErrRuleNotFound // 未启用时创建一律不可见（不泄露能力存在性）
	}
	if in.Source == nil && strings.TrimSpace(in.Note) == "" {
		return nil, 0, ErrNoteRequired
	}
	if strings.TrimSpace(in.ChannelId) == "" {
		return nil, 0, ErrPushRuleInvalidChannel
	}
	now := time.Now()
	var created *capture.PushMockRule
	version, err := m.pushRules.Mutate(ctx, app, did, func(rules []*capture.PushMockRule) ([]*capture.PushMockRule, bool, error) {
		// 2026-10-16（用户拍板）：push mock 模板池允许多条同域 enabled 模板共存，
		// 创建不再做域单活跃 409 冲突检查（模拟推送按规则 ID 快照，无匹配歧义）。
		var owner string
		if caller != nil {
			owner = caller.Username
		}
		created = &capture.PushMockRule{
			ID:                   id.ULID(),
			App:                  app,
			Did:                  did,
			ChannelId:            in.ChannelId,
			BizType:              in.BizType,
			Match:                in.Match,
			ReplacePayload:       in.ReplacePayload,
			ReplacePayloadBase64: in.ReplacePayloadBase64,
			Enabled:              in.Enabled,
			Note:                 in.Note,
			Source:               in.Source,
			CreatedAt:            now,
			UpdatedAt:            now,
			LastUsedAt:           now,
			Owner:                owner,
			UpdatedBy:            owner,
		}
		return append(rules, created), true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	views, _, _, err := m.evaluatePushRules(ctx, app, did)
	if err != nil {
		return nil, version, err
	}
	for _, v := range views {
		if v.ID == created.ID {
			return v, version, nil
		}
	}
	return &capture.PushMockRuleView{PushMockRule: created, Effective: in.Enabled}, version, nil
}

// UpdatePushMockRule edits a push rule's payload match, replace payload, note,
// enabled switch, and — since 2026-10-16（用户拍板放开）— the match key domain
// (ChannelId+BizType, pointer fields: nil = keep current). 模板池语义（同轮拍板）：
// 编辑迁移域/开启开关不再做域单活跃 409 冲突检查（多条同域 enabled 共存合法）。
// The source snapshot stays immutable. The update and the version bump run in ONE
// store lock. 权限：仅规则 owner 与 admin 可编辑/启停；
// 非 admin 对未知 ruleID 同样返回 ErrRuleForbidden（不泄露规则存在性）。
func (m *CaptureManager) UpdatePushMockRule(ctx context.Context, app, did, ruleID string, in *capture.UpdatePushMockRuleInput, caller *RuleCaller) (*capture.PushMockRuleView, int, error) {
	if m.pushRules == nil {
		return nil, 0, ErrRuleNotFound
	}
	var updated *capture.PushMockRule
	version, err := m.pushRules.Mutate(ctx, app, did, func(rules []*capture.PushMockRule) ([]*capture.PushMockRule, bool, error) {
		var existing *capture.PushMockRule
		for _, r := range rules {
			if r.ID == ruleID {
				existing = r
				break
			}
		}
		if existing == nil {
			if caller != nil && !caller.IsAdmin {
				return nil, false, ErrRuleForbidden
			}
			return nil, false, ErrRuleNotFound
		}
		if !canManageOwner(caller, existing.Owner) {
			return nil, false, ErrRuleForbidden
		}

		// 匹配键域编辑（2026-10-16 用户拍板放开）：指针字段区分「未提供」与
		// 「显式置空」。ChannelId 显式提供时不能为空（契约必填）。
		// 2026-10-16（用户拍板）：模板池允许多条同域共存，编辑迁移域/开启开关
		// 不再做域单活跃 409 冲突检查（模拟推送按规则 ID 快照，无匹配歧义）。
		targetChannel, targetBiz := existing.ChannelId, existing.BizType
		if in.ChannelId != nil {
			if strings.TrimSpace(*in.ChannelId) == "" {
				return nil, false, ErrPushRuleInvalidChannel
			}
			targetChannel = *in.ChannelId
		}
		if in.BizType != nil {
			targetBiz = *in.BizType
		}
		domainChanged := targetChannel != existing.ChannelId || targetBiz != existing.BizType

		// note 仅当 PUT 实际改动规则内容（匹配条件/替换 payload/匹配键域变化）时
		// 必填；纯开关操作可留空（对齐 HTTP UpdateMockRule 语义）。Note 为指针：
		// nil = 保持现值（纯 toggle 不清空既有备注）；改内容时未提供/置空 → 400。
		contentChanged := !reflect.DeepEqual(existing.Match, in.Match) ||
			existing.ReplacePayload != in.ReplacePayload ||
			existing.ReplacePayloadBase64 != in.ReplacePayloadBase64 ||
			domainChanged
		if in.Note == nil {
			if contentChanged {
				return nil, false, ErrNoteRequired
			}
		} else if contentChanged && strings.TrimSpace(*in.Note) == "" {
			return nil, false, ErrNoteRequired
		}

		c := *existing
		c.ChannelId = targetChannel
		c.BizType = targetBiz
		c.Match = in.Match
		c.ReplacePayload = in.ReplacePayload
		c.ReplacePayloadBase64 = in.ReplacePayloadBase64
		if in.Note != nil {
			c.Note = *in.Note
		}
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		c.UpdatedAt = time.Now()
		// 编辑或 toggle 计为"被使用"（滑动窗口清理基线）。
		c.LastUsedAt = c.UpdatedAt
		if caller != nil {
			c.UpdatedBy = caller.Username
		}
		updated = &c

		out := make([]*capture.PushMockRule, 0, len(rules))
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
	views, _, _, err := m.evaluatePushRules(ctx, app, did)
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

// DeletePushMockRule removes a push rule by ID (scoped to app/did). The delete
// and the version bump run in ONE store lock. 权限：仅规则 owner 与 admin 可删除；
// 非 admin 对未知 ruleID 掩码为 ErrRuleForbidden。
func (m *CaptureManager) DeletePushMockRule(ctx context.Context, app, did, ruleID string, caller *RuleCaller) (int, error) {
	if m.pushRules == nil {
		return 0, ErrRuleNotFound
	}
	return m.pushRules.Mutate(ctx, app, did, func(rules []*capture.PushMockRule) ([]*capture.PushMockRule, bool, error) {
		for i, r := range rules {
			if r.ID == ruleID {
				if !canManageOwner(caller, r.Owner) {
					return nil, false, ErrRuleForbidden
				}
				out := make([]*capture.PushMockRule, 0, len(rules)-1)
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
}

// ListPushMockRules returns every push rule of the device (Web view, including
// disabled) with the runtime Effective flag and the current push rule-set
// version. 模板池语义（2026-10-16 用户拍板）：Effective 即 enabled（多条同域
// enabled 共存全部 Effective），conflicts 恒空（保留 API 字段兼容）。
func (m *CaptureManager) ListPushMockRules(ctx context.Context, app, did string) ([]*capture.PushMockRuleView, []capture.PushMockRuleConflict, int, error) {
	return m.evaluatePushRules(ctx, app, did)
}

// ListActivePushMockRules is the SDK pull. When the device push rule-set version
// equals sinceVersion nothing changed: it returns no rules and changed=false.
// Otherwise it returns the Effective rules (模板池语义：Effective 即 enabled，同域
// 多条全部下发) and the new version.
func (m *CaptureManager) ListActivePushMockRules(ctx context.Context, app, did string, sinceVersion int) ([]*capture.PushMockRuleView, int, bool, error) {
	views, _, version, err := m.evaluatePushRules(ctx, app, did)
	if err != nil {
		return nil, 0, false, err
	}
	if version == sinceVersion {
		return nil, version, false, nil
	}
	active := make([]*capture.PushMockRuleView, 0, len(views))
	for _, v := range views {
		if v.Effective {
			active = append(active, v)
		}
	}
	return active, version, true, nil
}

// PushRuleVersion returns the current monotonic push rule-set version for
// (app, did). Used by the device heartbeat to tell the SDK whether it should
// pull an updated push rule snapshot.
func (m *CaptureManager) PushRuleVersion(ctx context.Context, app, did string) (int, error) {
	if m.pushRules == nil {
		return 0, nil
	}
	return m.pushRules.GetRuleVersion(ctx, app, did)
}

// RecordPushMockHit refreshes the hit statistics of a push rule (hitCount+1 /
// LastHitAt=now / LastUsedAt=now). Called when the SDK旁路采集上报 marked=true。
// Deliberately does NOT bump the push rule-set version — rule content is
// unchanged, so the SDK must not re-pull on every hit (对齐 HTTP touchHitRules
// 的 List+Update 模式，不 bump 版本)。Best-effort：store 错误记录日志，不上抛。
func (m *CaptureManager) RecordPushMockHit(ctx context.Context, app, did, ruleID string) {
	if m.pushRules == nil {
		return
	}
	all, err := m.pushRules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		m.log.Warn("push rule: list for hit record failed", "error", err)
		return
	}
	now := time.Now()
	for _, r := range all {
		if r.ID != ruleID {
			continue
		}
		r.HitCount++
		r.LastHitAt = now
		r.LastUsedAt = now
		if err := m.pushRules.Update(ctx, r); err != nil {
			m.log.Warn("push rule: record hit failed", "rule", r.ID, "error", err)
		}
		return
	}
	m.log.Warn("push rule: hit record skipped, rule not found", "rule", ruleID)
}

// ============================================================================
// Push rule retention janitor (对齐 HTTP ruleUsageTime / PurgeExpiredRules)
// ============================================================================

// pushRuleUsageTime picks the sliding-window baseline for a push rule:
// LastUsedAt, falling back to UpdatedAt (and then CreatedAt) for legacy rows.
func pushRuleUsageTime(r *capture.PushMockRule) time.Time {
	if !r.LastUsedAt.IsZero() {
		return r.LastUsedAt
	}
	if !r.UpdatedAt.IsZero() {
		return r.UpdatedAt
	}
	return r.CreatedAt
}

// PurgeExpiredPushRules deletes every push mock rule whose last use is older
// than the configured retention (default 7d, sliding window). A deleted rule
// bumps its device push rule-set version so the SDK drops it on the next
// heartbeat. Best-effort: per-rule errors are logged, not fatal.
func (m *CaptureManager) PurgeExpiredPushRules(ctx context.Context) {
	if m.pushRules == nil || m.cfg.MockRuleRetention <= 0 {
		return
	}
	all, err := m.pushRules.List(ctx, nil)
	if err != nil {
		m.log.Warn("push rule janitor: list failed", "error", err)
		return
	}
	now := time.Now()
	bumped := make(map[string]bool) // (app\0did) already bumped
	for _, r := range all {
		if now.Sub(pushRuleUsageTime(r)) <= m.cfg.MockRuleRetention {
			continue
		}
		if err := m.pushRules.Delete(ctx, r.ID); err != nil {
			m.log.Warn("push rule janitor: delete expired rule failed", "rule", r.ID, "error", err)
			continue
		}
		key := r.App + "\x00" + r.Did
		if !bumped[key] {
			if _, err := m.pushRules.BumpRuleVersion(ctx, r.App, r.Did); err != nil {
				m.log.Warn("push rule janitor: bump version failed", "app", r.App, "did", r.Did, "error", err)
			}
			bumped[key] = true
		}
		m.log.Info("push rule janitor: purged expired push mock rule",
			"rule", r.ID, "channelId", r.ChannelId, "bizType", r.BizType, "lastUsedAt", pushRuleUsageTime(r))
	}
}

// PurgeExpiredPushRuleCommands deletes pending push mock commands older than the
// retention window (default 7d, same as push rules). A command left unconsumed
// (device offline) must not accumulate forever; 7d far exceeds any realistic
// heartbeat window. Best-effort: errors are logged, not fatal.
func (m *CaptureManager) PurgeExpiredPushRuleCommands(ctx context.Context) {
	if m.pushRuleCommands == nil || m.cfg.MockRuleRetention <= 0 {
		return
	}
	n, err := m.pushRuleCommands.PurgeExpired(ctx, m.cfg.MockRuleRetention)
	if err != nil {
		m.log.Warn("push rule command janitor: purge failed", "error", err)
		return
	}
	if n > 0 {
		m.log.Info("push rule command janitor: purged expired push mock commands", "count", n)
	}
}
