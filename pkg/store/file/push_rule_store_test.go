package file

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// ============================================================================
// Push mock rules (能力线 L7) — store 层 + CaptureManager 层
// ============================================================================

// newPushManager wires a CaptureManager with the push rule store injected
// (L7 决策 D3：SetPushMockRules 注入模式；NewCaptureManager 签名不变)。
// 同时接线 LongConn store 以便 mocked 数据流（UploadLongConn → 命中统计）可测。
func newPushManager(t *testing.T) (*store.CaptureManager, *FileStore) {
	t.Helper()
	fs := newTestStore(t)
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	m.SetPushMockRules(fs.PushMockRules())
	m.SetLongConnStore(fs.LongConn())
	t.Cleanup(m.Stop)
	return m, fs
}

// pushInput builds a create payload. Capture-originated creates ("一键建规则")
// carry a source snapshot and may omit the note (对齐 HTTP MockRule D6 豁免)。
func pushInput(channelID, bizType string, enabled bool) *capture.PushMockRuleInput {
	return &capture.PushMockRuleInput{
		ChannelId:      channelID,
		BizType:        bizType,
		Match:          capture.PushMockMatch{Keyword: "tip"},
		ReplacePayload: `{"type":"chat","data":{"tip":"mocked"}}`,
		Enabled:        enabled,
		Source:         &capture.PushMockSource{ChannelId: channelID, BizType: bizType, MsgId: "1"},
	}
}

func TestPushRuleStore_MutateVersion(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()

	// Mutate 出错时存储保持原样（无半写入、无版本跳动）。
	if _, err := fs.PushMockRules().Mutate(ctx, "app", "d1", func(rules []*capture.PushMockRule) ([]*capture.PushMockRule, bool, error) {
		return nil, false, errors.New("boom")
	}); err == nil {
		t.Fatal("Mutate(error) = nil, want error")
	}
	if v, _ := fs.PushMockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("version after failed Mutate = %d, want 0", v)
	}
	if rules, _ := fs.PushMockRules().List(ctx, &store.MockRuleFilter{App: "app", Did: "d1"}); len(rules) != 0 {
		t.Fatalf("rules after failed Mutate = %d, want 0", len(rules))
	}

	// changed=false 的 Mutate 不 bump 版本。
	if _, err := fs.PushMockRules().Mutate(ctx, "app", "d1", func(rules []*capture.PushMockRule) ([]*capture.PushMockRule, bool, error) {
		return rules, false, nil
	}); err != nil {
		t.Fatalf("Mutate(noop) = %v", err)
	}
	if v, _ := fs.PushMockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("version after noop Mutate = %d, want 0", v)
	}

	// file 层 Create 不 bump 版本（版本仅由 Mutate/BumpRuleVersion 驱动，对齐
	// HTTP mockRuleStore）；HTTP 与 push 版本相互独立（互不污染）。
	if err := fs.PushMockRules().Create(ctx, &capture.PushMockRule{ID: "p1", App: "app", Did: "d1", ChannelId: "xcpush_tcp", Enabled: true}); err != nil {
		t.Fatalf("Create(p1) = %v", err)
	}
	if v, _ := fs.PushMockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("push version after file Create = %d, want 0 (bump only via Mutate)", v)
	}
	if v, _ := fs.MockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("HTTP rule version polluted by push create = %d, want 0", v)
	}

	// BumpRuleVersion 递增。
	if v, err := fs.PushMockRules().BumpRuleVersion(ctx, "app", "d1"); err != nil || v != 1 {
		t.Fatalf("BumpRuleVersion() = %d, %v; want 1, nil", v, err)
	}

	// 设备隔离：另一设备版本独立。
	if v, _ := fs.PushMockRules().GetRuleVersion(ctx, "app", "d2"); v != 0 {
		t.Fatalf("push version of d2 = %d, want 0", v)
	}
}

func TestPushRuleStore_PersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	newFS := func() *FileStore {
		fs := New(store.Config{DataDir: dir, ConfigDir: dir + "/config", CacheDir: dir + "/cache", StateDir: dir + "/state"})
		if err := fs.Open(context.Background()); err != nil {
			t.Fatalf("Open() = %v", err)
		}
		return fs
	}
	ctx := context.Background()

	fs := newFS()
	if err := fs.PushMockRules().Create(ctx, &capture.PushMockRule{
		ID: "p1", App: "app", Did: "d1", ChannelId: "xcpush_tcp", BizType: "1",
		Match: capture.PushMockMatch{Keyword: "tip"}, ReplacePayload: `{"ok":1}`, Enabled: true,
	}); err != nil {
		t.Fatalf("Create(p1) = %v", err)
	}
	if _, err := fs.PushMockRules().BumpRuleVersion(ctx, "app", "d1"); err != nil {
		t.Fatalf("BumpRuleVersion = %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// 重启：push 规则与版本从 data.json 恢复。
	fs2 := newFS()
	rules, err := fs2.PushMockRules().List(ctx, &store.MockRuleFilter{App: "app", Did: "d1"})
	if err != nil || len(rules) != 1 {
		t.Fatalf("after restart List() = %d rules, %v; want 1, nil", len(rules), err)
	}
	if rules[0].ID != "p1" || rules[0].ChannelId != "xcpush_tcp" || rules[0].ReplacePayload != `{"ok":1}` || !rules[0].Enabled {
		t.Fatalf("restored rule mismatch: %+v", rules[0])
	}
	if v, _ := fs2.PushMockRules().GetRuleVersion(ctx, "app", "d1"); v != 1 {
		t.Fatalf("restored push version = %d, want 1", v)
	}
	_ = fs2.Close()
}

func TestPushMockRule_CreateVersionAndEffective(t *testing.T) {
	m, fs := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	// 无 source 且空 note → ErrNoteRequired（手动创建规则语义锚点，对齐 HTTP）。
	_, _, err := m.CreatePushMockRule(ctx, "app", "d1", &capture.PushMockRuleInput{ChannelId: "xcpush_tcp", Enabled: true}, caller)
	if !errors.Is(err, store.ErrNoteRequired) {
		t.Fatalf("Create(no source, no note) = %v, want ErrNoteRequired", err)
	}
	// 空 channelId → ErrPushRuleInvalidChannel。
	_, _, err = m.CreatePushMockRule(ctx, "app", "d1", &capture.PushMockRuleInput{Note: "n", Enabled: true}, caller)
	if !errors.Is(err, store.ErrPushRuleInvalidChannel) {
		t.Fatalf("Create(empty channelId) = %v, want ErrPushRuleInvalidChannel", err)
	}

	v1, version, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if err != nil {
		t.Fatalf("Create(p1) = %v", err)
	}
	if !v1.Effective {
		t.Fatal("single enabled rule must be Effective")
	}
	if v1.Owner != "alice" || v1.UpdatedBy != "alice" {
		t.Fatalf("owner stamp missing: owner=%q updatedBy=%q", v1.Owner, v1.UpdatedBy)
	}
	if version != 1 {
		t.Fatalf("version after first create = %d, want 1", version)
	}

	// 同域（channelId+bizType）再开一条 enabled → 409 冲突；effective 全 false + conflicts 报告。
	_, _, err = m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if !errors.Is(err, store.ErrRuleConflict) {
		t.Fatalf("Create(second enabled same domain) = %v, want ErrRuleConflict", err)
	}
	// 不同 bizType 域可以同时开（互斥域 = channelId+bizType）。
	v2, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "2", true), caller)
	if err != nil {
		t.Fatalf("Create(p2 different bizType) = %v", err)
	}
	if !v2.Effective {
		t.Fatal("rule in a different bizType domain must be Effective")
	}

	// 异常域冲突报告：多开状态正常流程被 409 挡住（与 HTTP 一致），只能由
	// 历史/并发遗留数据产生——file 层直插第二条同域 enabled 规则模拟。
	if err := fs.PushMockRules().Create(ctx, &capture.PushMockRule{
		ID: "p3", App: "app", Did: "d1", ChannelId: "xcpush_tcp", BizType: "1", Enabled: true,
	}); err != nil {
		t.Fatalf("store Create(p3, same domain) = %v", err)
	}
	views, conflicts, _, err := m.ListPushMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ListPushMockRules = %v", err)
	}
	byID := map[string]*capture.PushMockRuleView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if byID[v1.ID].Effective || byID["p3"].Effective {
		t.Fatal("multi-enabled domain rules must be non-Effective")
	}
	if len(conflicts) != 1 || conflicts[0].ChannelId != "xcpush_tcp" || conflicts[0].BizType != "1" {
		t.Fatalf("conflicts = %+v, want one domain xcpush_tcp/1", conflicts)
	}
}

func TestPushMockRule_UpdateSemantics(t *testing.T) {
	m, _ := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	v, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", false), caller)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	// 内容变化（Match/ReplacePayload 改变）但 note 空 → ErrNoteRequired。
	_, _, err = m.UpdatePushMockRule(ctx, "app", "d1", v.ID, &capture.UpdatePushMockRuleInput{
		Match: capture.PushMockMatch{Keyword: "changed"}, ReplacePayload: `{"x":1}`,
	}, caller)
	if !errors.Is(err, store.ErrNoteRequired) {
		t.Fatalf("Update(content changed, empty note) = %v, want ErrNoteRequired", err)
	}

	// 纯 toggle（内容原样回传，note 空）合法，且 bump 版本。
	upd, _, err := m.UpdatePushMockRule(ctx, "app", "d1", v.ID, &capture.UpdatePushMockRuleInput{
		Match: capture.PushMockMatch{Keyword: "tip"}, ReplacePayload: `{"type":"chat","data":{"tip":"mocked"}}`, Enabled: boolPtr(true),
	}, caller)
	if err != nil {
		t.Fatalf("Update(toggle on) = %v", err)
	}
	if !upd.Enabled || !upd.Effective {
		t.Fatal("toggled rule must be enabled and effective")
	}

	// 匹配键 frozen：UpdatePushMockRuleInput 无 ChannelId/BizType 字段——
	// 更新后 ChannelId 保持原值（编译期约束 + 运行时验证）。
	if upd.ChannelId != "xcpush_tcp" || upd.BizType != "1" {
		t.Fatalf("match key mutated: %+v", upd.PushMockRule)
	}
}

func TestPushMockRule_Permissions(t *testing.T) {
	m, _ := newPushManager(t)
	ctx := context.Background()
	alice := &store.RuleCaller{Username: "alice", IsAdmin: false}
	bob := &store.RuleCaller{Username: "bob", IsAdmin: false}
	admin := &store.RuleCaller{Username: "root", IsAdmin: true}

	v, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), alice)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	// 非 owner 编辑/删除 → ErrRuleForbidden。
	if _, _, err = m.UpdatePushMockRule(ctx, "app", "d1", v.ID, &capture.UpdatePushMockRuleInput{Note: "x", Enabled: boolPtr(false)}, bob); !errors.Is(err, store.ErrRuleForbidden) {
		t.Fatalf("Update(by bob) = %v, want ErrRuleForbidden", err)
	}
	if _, err = m.DeletePushMockRule(ctx, "app", "d1", v.ID, bob); !errors.Is(err, store.ErrRuleForbidden) {
		t.Fatalf("Delete(by bob) = %v, want ErrRuleForbidden", err)
	}
	// 未知 ruleID 对非 admin 掩码为 ErrRuleForbidden（不泄露存在性）。
	if _, _, err = m.UpdatePushMockRule(ctx, "app", "d1", "ghost", &capture.UpdatePushMockRuleInput{Note: "x"}, bob); !errors.Is(err, store.ErrRuleForbidden) {
		t.Fatalf("Update(ghost, by bob) = %v, want ErrRuleForbidden", err)
	}

	// owner 可删除。
	if _, err = m.DeletePushMockRule(ctx, "app", "d1", v.ID, alice); err != nil {
		t.Fatalf("Delete(by owner) = %v", err)
	}
	// admin 可管理 legacy 规则（owner 空）。
	v2, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "2", true), nil)
	if err != nil {
		t.Fatalf("Create(no caller) = %v", err)
	}
	if v2.Owner != "" {
		t.Fatalf("legacy rule owner = %q, want empty", v2.Owner)
	}
	if _, _, err = m.UpdatePushMockRule(ctx, "app", "d1", v2.ID, &capture.UpdatePushMockRuleInput{Enabled: boolPtr(false)}, bob); !errors.Is(err, store.ErrRuleForbidden) {
		t.Fatalf("Update(legacy, by bob) = %v, want ErrRuleForbidden", err)
	}
	// admin 更新：Update 是全量语义，须回传原内容（对齐 HTTP UpdateMockRuleInput）。
	if _, _, err = m.UpdatePushMockRule(ctx, "app", "d1", v2.ID, &capture.UpdatePushMockRuleInput{
		Match: capture.PushMockMatch{Keyword: "tip"}, ReplacePayload: `{"type":"chat","data":{"tip":"mocked"}}`, Enabled: boolPtr(false),
	}, admin); err != nil {
		t.Fatalf("Update(legacy, by admin) = %v", err)
	}
}

func TestPushMockRule_ListActiveSinceVersion(t *testing.T) {
	m, _ := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	_, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	// 停用规则（有效集不含）。
	if _, _, err = m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "2", false), caller); err != nil {
		t.Fatalf("Create(disabled) = %v", err)
	}

	// sinceVersion=当前版本 → 无变化（两次创建后版本已推进）。
	cur, _ := m.PushRuleVersion(ctx, "app", "d1")
	active, v, changed, err := m.ListActivePushMockRules(ctx, "app", "d1", cur)
	if err != nil || changed || len(active) != 0 {
		t.Fatalf("pull(same version) = %d rules changed=%v err=%v; want 0 false nil", len(active), changed, err)
	}
	if v != cur {
		t.Fatalf("pull(same version) version = %d, want %d", v, cur)
	}
	// sinceVersion=0 → 只返回 effective（停用规则不下发）。
	active, v, changed, err = m.ListActivePushMockRules(ctx, "app", "d1", 0)
	if err != nil || !changed || len(active) != 1 {
		t.Fatalf("pull(from 0) = %d rules changed=%v err=%v; want 1 true nil", len(active), changed, err)
	}
	if active[0].ChannelId != "xcpush_tcp" || active[0].BizType != "1" {
		t.Fatalf("pulled rule mismatch: %+v", active[0])
	}
	// owner 等 Web-only 字段的裁剪在 handler 线格式层（L7.1b toSDKRules，
	// 对齐 HTTP sdkRuleView）——manager 层返回完整 view 与 HTTP 一致。
	// PushRuleVersion 与 HTTP RuleVersion 独立。
	if pv, _ := m.PushRuleVersion(ctx, "app", "d1"); pv != v {
		t.Fatalf("PushRuleVersion = %d, want %d", pv, v)
	}
	if hv, _ := m.RuleVersion(ctx, "app", "d1"); hv != 0 {
		t.Fatalf("HTTP RuleVersion polluted = %d, want 0", hv)
	}
}

func TestPushMockRule_RecordHit(t *testing.T) {
	m, _ := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	v, version, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	m.RecordPushMockHit(ctx, "app", "d1", v.ID)
	m.RecordPushMockHit(ctx, "app", "d1", v.ID)

	views, _, _, err := m.ListPushMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	var hit *capture.PushMockRuleView
	for _, w := range views {
		if w.ID == v.ID {
			hit = w
		}
	}
	if hit == nil || hit.HitCount != 2 || hit.LastHitAt.IsZero() {
		t.Fatalf("hit stats = %+v, want HitCount=2 and LastHitAt set", hit)
	}
	// 命中统计不 bump 版本（SDK 不应因命中重拉）。
	if v2, _ := m.PushRuleVersion(ctx, "app", "d1"); v2 != version {
		t.Fatalf("version after hits = %d, want unchanged %d", v2, version)
	}
	// 未知 ruleID 不 panic、仅日志。
	m.RecordPushMockHit(ctx, "app", "d1", "ghost")
}

func TestPushMockRule_PurgeExpired(t *testing.T) {
	m, fs := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	v, _, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	// 过期规则：把 LastUsedAt 回拨超过 retention（默认 7 天）——List 返回副本，
	// 经 store Update 写回。
	views, _, _, _ := m.ListPushMockRules(ctx, "app", "d1")
	for _, w := range views {
		if w.ID == v.ID {
			w.LastUsedAt = time.Now().Add(-8 * 24 * time.Hour)
			if err := fs.PushMockRules().Update(ctx, w.PushMockRule); err != nil {
				t.Fatalf("backdate LastUsedAt = %v", err)
			}
		}
	}

	before, _ := m.PushRuleVersion(ctx, "app", "d1")
	m.PurgeExpiredPushRules(ctx)
	after, _ := m.PushRuleVersion(ctx, "app", "d1")
	remaining, _, _, _ := m.ListPushMockRules(ctx, "app", "d1")
	if len(remaining) != 0 {
		t.Fatalf("purge left %d rules, want 0", len(remaining))
	}
	if after != before+1 {
		t.Fatalf("purge version = %d, want %d (bumped)", after, before+1)
	}
}

// TestPushMockRule_HitViaLongConnUpload: marked 数据流——SDK 上传 mocked=true
// + mockRuleId 的记录 → 规则 hitCount/lastHitAt 递增，push 版本不 bump
// （规则内容未变，SDK 不应因命中重拉）。
func TestPushMockRule_HitViaLongConnUpload(t *testing.T) {
	m, _ := newPushManager(t)
	ctx := context.Background()
	caller := &store.RuleCaller{Username: "alice", IsAdmin: false}

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	v, version, err := m.CreatePushMockRule(ctx, "app", "d1", pushInput("xcpush_tcp", "1", true), caller)
	if err != nil {
		t.Fatalf("CreatePushMockRule = %v", err)
	}

	// 上传 2 条 mocked 命中记录 + 1 条未命中记录。
	now := time.Now()
	_, err = m.UploadLongConn(ctx, s.ID, capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records: []capture.LongConnectionRecordInput{
			{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: now, MsgID: "m1", PayloadText: `{"tip":"x"}`, Mocked: true, MockRuleID: v.ID},
			{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: now, MsgID: "m2", PayloadText: `{"tip":"y"}`, Mocked: true, MockRuleID: v.ID},
			{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: now, MsgID: "m3", PayloadText: `{"tip":"z"}`},
		},
	})
	if err != nil {
		t.Fatalf("UploadLongConn = %v", err)
	}

	views, _, _, err := m.ListPushMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ListPushMockRules = %v", err)
	}
	var hit *capture.PushMockRuleView
	for _, w := range views {
		if w.ID == v.ID {
			hit = w
		}
	}
	if hit == nil || hit.HitCount != 2 || hit.LastHitAt.IsZero() {
		t.Fatalf("hit stats = %+v, want HitCount=2 and LastHitAt set", hit)
	}
	// 版本不 bump。
	if cur, _ := m.PushRuleVersion(ctx, "app", "d1"); cur != version {
		t.Fatalf("version after hit upload = %d, want %d", cur, version)
	}
	// mocked 标识随记录持久化（Web 展示 "Mock 命中"）。
	entries, _, err := m.ListSessionLongConn(ctx, s.ID, capture.LongConnectionQuery{})
	if err != nil {
		t.Fatalf("ListSessionLongConn = %v", err)
	}
	mockedSeen := 0
	for _, e := range entries {
		if e.Mocked {
			mockedSeen++
			if e.MockRuleID != v.ID {
				t.Fatalf("mockRuleId = %q, want %q", e.MockRuleID, v.ID)
			}
		}
	}
	if mockedSeen != 2 {
		t.Fatalf("mocked records persisted = %d, want 2", mockedSeen)
	}
}

func TestPushRuleCommandStore_AppendListMarkConsumed(t *testing.T) {
	fs := newTestStore(t)
	cmds := fs.PushRuleCommands()
	ctx := context.Background()

	now := time.Now()
	if err := cmds.Append(ctx, "app", "d1", &capture.PushRuleCommand{ID: "c1", RuleID: "r1", ChannelId: "xcpush_tcp", BizType: "1", Payload: `{"a":1}`, CreatedAt: now}); err != nil {
		t.Fatalf("Append(c1) = %v", err)
	}
	if err := cmds.Append(ctx, "app", "d1", &capture.PushRuleCommand{ID: "c2", RuleID: "r2", ChannelId: "xcpush_tcp", BizType: "2", Payload: `{"b":2}`, CreatedAt: now}); err != nil {
		t.Fatalf("Append(c2) = %v", err)
	}

	pending, err := cmds.ListPending(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ListPending = %v", err)
	}
	if len(pending) != 2 || pending[0].ID != "c1" {
		t.Fatalf("pending = %+v, want [c1 c2] in order", pending)
	}

	// 设备隔离：d2 无指令。
	if p2, _ := cmds.ListPending(ctx, "app", "d2"); len(p2) != 0 {
		t.Fatalf("d2 pending = %d, want 0", len(p2))
	}

	// ack 部分消费。
	if err := cmds.MarkConsumed(ctx, "app", "d1", []string{"c1"}); err != nil {
		t.Fatalf("MarkConsumed(c1) = %v", err)
	}
	pending, _ = cmds.ListPending(ctx, "app", "d1")
	if len(pending) != 1 || pending[0].ID != "c2" {
		t.Fatalf("pending after ack = %+v, want [c2]", pending)
	}

	// ack 幂等：未知 id 忽略、重复 ack 无副作用。
	if err := cmds.MarkConsumed(ctx, "app", "d1", []string{"nope", "c2"}); err != nil {
		t.Fatalf("MarkConsumed(idempotent) = %v", err)
	}
	if p, _ := cmds.ListPending(ctx, "app", "d1"); len(p) != 0 {
		t.Fatalf("pending after full ack = %d, want 0", len(p))
	}
}

func TestPushRuleCommandStore_PurgeExpiredAndQueueCap(t *testing.T) {
	fs := newTestStore(t)
	cmds := fs.PushRuleCommands()
	ctx := context.Background()

	// 队列上限：超 100 条丢弃最旧。
	for i := 0; i < 105; i++ {
		if err := cmds.Append(ctx, "app", "d1", &capture.PushRuleCommand{
			ID: "c" + strconv.Itoa(i), RuleID: "r1", ChannelId: "xcpush_tcp", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Append #%d = %v", i, err)
		}
	}
	pending, _ := cmds.ListPending(ctx, "app", "d1")
	if len(pending) != 100 || pending[0].ID != "c5" || pending[99].ID != "c104" {
		t.Fatalf("queue cap = %d entries [%s..%s], want 100 [c5..c104]", len(pending), pending[0].ID, pending[len(pending)-1].ID)
	}

	// 过期清理：仅删超窗口指令。
	fs2 := newTestStore(t)
	cmds2 := fs2.PushRuleCommands()
	if err := cmds2.Append(ctx, "app", "d1", &capture.PushRuleCommand{ID: "old", CreatedAt: time.Now().Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatalf("Append(old) = %v", err)
	}
	if err := cmds2.Append(ctx, "app", "d1", &capture.PushRuleCommand{ID: "fresh", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Append(fresh) = %v", err)
	}
	n, err := cmds2.PurgeExpired(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeExpired = %v", err)
	}
	if n != 1 {
		t.Fatalf("purged = %d, want 1", n)
	}
	if left, _ := cmds2.ListPending(ctx, "app", "d1"); len(left) != 1 || left[0].ID != "fresh" {
		t.Fatalf("left = %+v, want [fresh]", left)
	}
}
