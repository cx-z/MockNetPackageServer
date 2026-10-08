package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

//  分享持久化补充测试：7 天 TTL 的过期拒绝（懒检查）与 janitor 清理跨重启。
// 重启恢复本身由 TestCaptureManager_SharePersistsAcrossRestart 覆盖（capture_registry_test.go）。

func TestShare_ExpiredTTL_GetShareRejects(t *testing.T) {
	m, fs := newCaptureManager(t, 0)
	ctx := context.Background()

	now := time.Now()
	// 直接种一条已过期分享（模拟 7 天 TTL 流逝）。
	if err := fs.Shares().Create(ctx, &store.ShareSnapshot{
		ShareID:   "expired-share-1",
		CreatedAt: now.Add(-8 * 24 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
		Entry:     &capture.TrafficEntry{ID: "t1", Method: "GET", URL: "http://x/a"},
	}); err != nil {
		t.Fatalf("seed expired share: %v", err)
	}
	// 有效分享对照（未过期）。
	if err := fs.Shares().Create(ctx, &store.ShareSnapshot{
		ShareID:   "valid-share-1",
		CreatedAt: now,
		ExpiresAt: now.Add(store.ShareTTL),
		Entry:     &capture.TrafficEntry{ID: "t2", Method: "GET", URL: "http://x/b"},
	}); err != nil {
		t.Fatalf("seed valid share: %v", err)
	}

	// 过期分享 → 懒检查拒绝（TTL 语义：7 天契约失效）。
	if _, err := m.GetShare(ctx, "expired-share-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetShare(expired) = %v, want ErrNotFound", err)
	}
	// 有效分享仍可访问。
	got, err := m.GetShare(ctx, "valid-share-1")
	if err != nil {
		t.Fatalf("GetShare(valid) = %v", err)
	}
	if got.Entry == nil || got.Entry.ID != "t2" {
		t.Fatalf("GetShare(valid) entry = %+v, want t2", got.Entry)
	}
}

// janitor 清理：PurgeExpiredShares 只删过期；跨重启后过期分享彻底消失、有效分享仍在。
func TestShare_ExpiredTTL_PurgeAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	newFS := func() *FileStore {
		fs := New(store.Config{
			DataDir:   dir,
			ConfigDir: dir + "/config",
			CacheDir:  dir + "/cache",
			StateDir:  dir + "/state",
		})
		if err := fs.Open(context.Background()); err != nil {
			t.Fatalf("Open() = %v", err)
		}
		return fs
	}
	ctx := context.Background()

	fs := newFS()
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	now := time.Now()
	_ = fs.Shares().Create(ctx, &store.ShareSnapshot{
		ShareID: "expired-2", CreatedAt: now.Add(-9 * 24 * time.Hour), ExpiresAt: now.Add(-2 * time.Hour),
		Entry: &capture.TrafficEntry{ID: "e1", Method: "GET", URL: "http://x/e"},
	})
	_ = fs.Shares().Create(ctx, &store.ShareSnapshot{
		ShareID: "valid-2", CreatedAt: now, ExpiresAt: now.Add(store.ShareTTL),
		Entry: &capture.TrafficEntry{ID: "v1", Method: "GET", URL: "http://x/v"},
	})
	m.Stop()
	if err := fs.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// 重启：janitor 路径（PurgeExpiredShares）清理过期分享；Close 确保落盘。
	fs2 := newFS()
	m2 := store.NewCaptureManager(fs2.Devices(), fs2.CaptureSessions(), fs2.MockRules(), fs2.PairingTokens(), fs2.Shares(), store.DefaultCaptureConfig())
	m2.PurgeExpiredShares(ctx)
	m2.Stop()
	if err := fs2.Close(); err != nil {
		t.Fatalf("Close(fs2) = %v", err)
	}

	// 第三次重启，确认清理结果已持久化（文件里没有过期分享）。
	fs3 := newFS()
	defer fs3.Close()
	all, err := fs3.Shares().List(ctx)
	if err != nil {
		t.Fatalf("ListShares = %v", err)
	}
	if len(all) != 1 || all[0].ShareID != "valid-2" {
		t.Fatalf("after purge+restart shares = %+v, want only valid-2", all)
	}
}
