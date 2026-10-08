package file

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

//  模型：MockRule.owner/updatedBy 写入与 data.json 持久化。

func TestMockRule_OwnerStampOnCreate(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}

	// 创建时从当前会话用户写入 owner（创建后自己即 owner）；updatedBy 初始同 owner。
	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/owned", false),
		&store.RuleCaller{Username: "alice", IsAdmin: false})
	if err != nil {
		t.Fatalf("CreateMockRule(alice) = %v", err)
	}
	if r.Owner != "alice" {
		t.Errorf("rule.Owner = %q, want %q", r.Owner, "alice")
	}
	if r.UpdatedBy != "alice" {
		t.Errorf("rule.UpdatedBy = %q, want %q (initial = owner)", r.UpdatedBy, "alice")
	}

	// 另一用户创建 → owner 各自独立。
	r2, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/other", false),
		&store.RuleCaller{Username: "bob", IsAdmin: false})
	if err != nil {
		t.Fatalf("CreateMockRule(bob) = %v", err)
	}
	if r2.Owner != "bob" {
		t.Errorf("r2.Owner = %q, want %q", r2.Owner, "bob")
	}

	// --no-auth（caller=nil）→ owner 为空（存量规则形态， 仅 admin 可管理）。
	r3, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("DELETE", "/api/legacy", false), nil)
	if err != nil {
		t.Fatalf("CreateMockRule(nil) = %v", err)
	}
	if r3.Owner != "" || r3.UpdatedBy != "" {
		t.Errorf("no-auth rule owner/updatedBy = %q/%q, want empty", r3.Owner, r3.UpdatedBy)
	}
}

func TestMockRule_UpdatedByStampOnUpdate(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/owned", false),
		&store.RuleCaller{Username: "alice", IsAdmin: false})
	if err != nil {
		t.Fatalf("CreateMockRule() = %v", err)
	}

	// alice 自己编辑 → updatedBy 仍为 alice。
	u1, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID,
		updateInput(`{"v":2}`, "edit by alice", boolPtr(true)),
		&store.RuleCaller{Username: "alice", IsAdmin: false})
	if err != nil {
		t.Fatalf("UpdateMockRule(alice) = %v", err)
	}
	if u1.UpdatedBy != "alice" {
		t.Errorf("updatedBy = %q, want %q", u1.UpdatedBy, "alice")
	}
	if u1.Owner != "alice" {
		t.Errorf("owner changed on update = %q, want %q (owner is immutable)", u1.Owner, "alice")
	}

	// admin 修改 → updatedBy 记录最后修改者（owner 不变； 权限：非 owner 需 admin）。
	u2, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID,
		updateInput(`{"v":3}`, "edit by bob", boolPtr(false)),
		&store.RuleCaller{Username: "bob", IsAdmin: true})
	if err != nil {
		t.Fatalf("UpdateMockRule(admin bob) = %v", err)
	}
	if u2.UpdatedBy != "bob" {
		t.Errorf("updatedBy = %q, want %q", u2.UpdatedBy, "bob")
	}
	if u2.Owner != "alice" {
		t.Errorf("owner changed on update = %q, want %q", u2.Owner, "alice")
	}
}

//  data.json 持久化：owner/updatedBy 随规则落盘，重启后仍在（纯服务端字段
// 也要跨重启存活，权限语义依赖它）。
func TestMockRule_OwnerPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	newFS := func() *FileStore {
		fs := New(store.Config{
			DataDir:   dir,
			ConfigDir: filepath.Join(dir, "config"),
			CacheDir:  filepath.Join(dir, "cache"),
			StateDir:  filepath.Join(dir, "state"),
		})
		if err := fs.Open(context.Background()); err != nil {
			t.Fatalf("Open() failed: %v", err)
		}
		return fs
	}
	ctx := context.Background()

	fs := newFS()
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/persist", false),
		&store.RuleCaller{Username: "alice", IsAdmin: false}); err != nil {
		t.Fatalf("CreateMockRule() = %v", err)
	}
	m.Stop()
	if err := fs.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// 重启：同一数据目录，全新 FileStore + CaptureManager。
	fs2 := newFS()
	defer fs2.Close()
	m2 := store.NewCaptureManager(fs2.Devices(), fs2.CaptureSessions(), fs2.MockRules(), fs2.PairingTokens(), fs2.Shares(), store.DefaultCaptureConfig())
	defer m2.Stop()

	rules, _, _, err := m2.ListMockRules(ctx, "app", "d1")
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListMockRules(after restart) = %d, %v; want 1", len(rules), err)
	}
	if rules[0].Owner != "alice" {
		t.Errorf("owner lost after restart = %q, want %q", rules[0].Owner, "alice")
	}
	if rules[0].UpdatedBy != "alice" {
		t.Errorf("updatedBy lost after restart = %q, want %q", rules[0].UpdatedBy, "alice")
	}
}
