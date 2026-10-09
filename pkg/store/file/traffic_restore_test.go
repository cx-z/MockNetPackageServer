package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// newManagerOn opens a FileStore at dir and returns a CaptureManager wired
// with the per-session traffic archive store (跨重启保留请求存档).
func newManagerOn(t *testing.T, dir string, cfg store.CaptureConfig) (*store.CaptureManager, *FileStore) {
	t.Helper()
	fs := openFileStore(t, dir)
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), cfg)
	m.SetTrafficStore(fs.Traffic())
	t.Cleanup(m.Stop)
	return m, fs
}

// archivePath returns the on-disk archive file for a session.
func archivePath(dir, sid string) string {
	return filepath.Join(dir, "traffic", sid+".json")
}

func TestCaptureManager_ArchiveSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.DefaultCaptureConfig()
	cfg.TrafficRetention = 2 * time.Hour // long enough for this test

	m1, fs1 := newManagerOn(t, dir, cfg)
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	base := time.Now()
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", base),
		trafficEntry("GET", "http://a.com/2", base.Add(time.Second)),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	// End the session: the archive stays for the retention window.
	if err := m1.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// Restart: restore must bring the request history back.
	m2, fs2 := newManagerOn(t, dir, cfg)
	defer fs2.Close()
	m2.RestoreTraffic(ctx)

	got, total, err := m2.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic(after restore) = %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("after restore total=%d len=%d, want 2/2", total, len(got))
	}
	if _, err := m2.GetTraffic(ctx, got[0].ID); err != nil {
		t.Errorf("GetTraffic(restored) = %v, want present", err)
	}
	// Session record survived too, so ownership still resolves.
	if app, did, err := m2.GetTrafficWithOwner(ctx, got[0].ID); err != nil || app != "app" || did != "d1" {
		t.Errorf("GetTrafficWithOwner(after restore) = (%q,%q,%v), want (app,d1,nil)", app, did, err)
	}
}

func TestCaptureManager_Restore_SeqContinues(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m1, fs1 := newManagerOn(t, dir, store.DefaultCaptureConfig())
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", time.Now()),
		trafficEntry("GET", "http://a.com/2", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	_ = fs1.Close()

	m2, fs2 := newManagerOn(t, dir, store.DefaultCaptureConfig())
	defer fs2.Close()
	m2.RestoreTraffic(ctx)
	// New uploads after restore must continue the per-session seq from the max
	// restored value, not restart at 1.
	if _, err := m2.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("POST", "http://a.com/3", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic(after restore) = %v", err)
	}
	got, _, err := m2.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil || len(got) != 3 {
		t.Fatalf("ListSessionTraffic() = %d, %v; want 3", len(got), err)
	}
	if got[2].Seq != 3 {
		t.Errorf("last seq = %d, want 3 (continue after restore)", got[2].Seq)
	}
}

func TestCaptureManager_ArchiveFileFollowsWindowCap(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.DefaultCaptureConfig()
	cfg.MaxSessionTrafficEntries = 5
	m1, fs1 := newManagerOn(t, dir, cfg)
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	base := time.Now()
	entries := make([]*capture.TrafficEntry, 8)
	for i := range entries {
		entries[i] = trafficEntry("GET", "http://a.com/"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Millisecond))
	}
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, entries); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	// In-memory window trimmed to the cap.
	_, total, err := m1.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil || total != 5 {
		t.Fatalf("ListSessionTraffic() total = %d, %v; want 5 (window cap)", total, err)
	}
	// The archive file matches the trimmed slice (内存与文件同源).
	loaded, err := fs1.Traffic().LoadAllSessionTraffic(ctx)
	if err != nil || len(loaded[s.ID]) != 5 {
		t.Fatalf("archive entries = %d, %v; want 5 (same cap)", len(loaded[s.ID]), err)
	}
}

func TestCaptureManager_Clear_SyncsArchive(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m1, fs1 := newManagerOn(t, dir, store.DefaultCaptureConfig())
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if err := m1.ClearSessionTraffic(ctx, s.ID); err != nil {
		t.Fatalf("ClearSessionTraffic() = %v", err)
	}
	if _, err := os.Stat(archivePath(dir, s.ID)); !os.IsNotExist(err) {
		t.Errorf("archive not removed after clear: %v", err)
	}
	_ = fs1.Close()

	// Restart: cleared logs must not resurrect.
	m2, fs2 := newManagerOn(t, dir, store.DefaultCaptureConfig())
	defer fs2.Close()
	m2.RestoreTraffic(ctx)
	_, total, err := m2.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	if total != 0 {
		t.Errorf("cleared logs resurrected after restart: total = %d, want 0", total)
	}
}

func TestCaptureManager_DeleteTraffic_SyncsArchive(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m1, fs1 := newManagerOn(t, dir, store.DefaultCaptureConfig())
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", time.Now()),
		trafficEntry("GET", "http://a.com/2", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	got, _, _ := m1.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	firstID := got[0].ID
	if err := m1.DeleteTraffic(ctx, firstID); err != nil {
		t.Fatalf("DeleteTraffic() = %v", err)
	}
	loaded, err := fs1.Traffic().LoadAllSessionTraffic(ctx)
	if err != nil || len(loaded[s.ID]) != 1 {
		t.Fatalf("archive after delete = %d entries, %v; want 1", len(loaded[s.ID]), err)
	}
	if loaded[s.ID][0].ID == firstID {
		t.Error("deleted entry still present in archive")
	}
}

func TestCaptureManager_Purge_DropsArchiveButShareSurvives(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.DefaultCaptureConfig()
	cfg.TrafficRetention = 50 * time.Millisecond
	m1, _ := newManagerOn(t, dir, cfg)
	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m1.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	got, _, _ := m1.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	share, err := m1.CreateShare(ctx, got[0].ID)
	if err != nil {
		t.Fatalf("CreateShare() = %v", err)
	}
	if err := m1.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	m1.PurgeExpiredEndedSessions(ctx)

	// The 48h-expired session's archive file is gone with the in-memory traffic.
	if _, err := os.Stat(archivePath(dir, s.ID)); !os.IsNotExist(err) {
		t.Errorf("archive not removed after janitor purge: %v", err)
	}
	if _, err := m1.GetSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("GetSession(after purge) = %v, want ErrSessionNotFound", err)
	}
	// The share snapshot is an independent 7-day copy: still readable.
	snap, err := m1.GetShare(ctx, share.ShareID)
	if err != nil {
		t.Errorf("GetShare(after purge) = %v, want still valid (7d independent snapshot)", err)
	}
	if snap != nil && snap.Entry == nil {
		t.Error("share entry missing")
	}
}

func TestCaptureManager_Restore_RemovesOrphanAndExpired(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.DefaultCaptureConfig()

	fs0 := openFileStore(t, dir)
	ts := fs0.Traffic()
	// Orphan archive: no session record.
	if err := ts.SaveSessionTraffic(ctx, "orphan-sid", []*capture.TrafficEntry{trafficEntry("GET", "http://o.com", time.Now())}); err != nil {
		t.Fatalf("SaveSessionTraffic(orphan) = %v", err)
	}
	m0 := store.NewCaptureManager(fs0.Devices(), fs0.CaptureSessions(), fs0.MockRules(), fs0.PairingTokens(), fs0.Shares(), cfg)
	m0.SetTrafficStore(ts)
	if _, err := m0.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	// Expired-ended session: record exists but its 48h window already passed.
	s, _, err := m0.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	sc, err := m0.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession() = %v", err)
	}
	endedAt := time.Now().Add(-time.Hour)
	retainUntil := time.Now().Add(-time.Minute)
	sc.Status = capture.SessionStatusEnded
	sc.EndedAt = &endedAt
	sc.RetainUntil = &retainUntil
	if err := fs0.CaptureSessions().Update(ctx, sc); err != nil {
		t.Fatalf("Update(expired) = %v", err)
	}
	if err := ts.SaveSessionTraffic(ctx, s.ID, []*capture.TrafficEntry{trafficEntry("GET", "http://e.com", time.Now())}); err != nil {
		t.Fatalf("SaveSessionTraffic(expired) = %v", err)
	}
	// Live capturing session: its archive must be kept.
	s2, _, err := m0.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession(live) = %v", err)
	}
	if _, err := m0.UploadTraffic(ctx, "app", "d1", s2.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://live.com/1", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic(live) = %v", err)
	}
	_ = fs0.Close()

	m2, fs2 := newManagerOn(t, dir, cfg)
	defer fs2.Close()
	m2.RestoreTraffic(ctx)

	// Orphan + expired archives removed; live archive restored.
	if _, err := os.Stat(archivePath(dir, "orphan-sid")); !os.IsNotExist(err) {
		t.Errorf("orphan archive not removed on restore: %v", err)
	}
	if _, err := os.Stat(archivePath(dir, s.ID)); !os.IsNotExist(err) {
		t.Errorf("expired archive not removed on restore: %v", err)
	}
	got, total, err := m2.ListSessionTraffic(ctx, s2.ID, 0, 0, store.TrafficFilter{})
	if err != nil || total != 1 {
		t.Fatalf("live session traffic = %d, %v; want 1 (kept across restore)", total, err)
	}
	_ = got
}

func TestCaptureManager_NilTrafficStore_NoArchiveWritten(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fs := openFileStore(t, dir)
	defer fs.Close()
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	t.Cleanup(m.Stop)
	// No SetTrafficStore: memory-only behavior preserved.
	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		trafficEntry("GET", "http://a.com/1", time.Now()),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "traffic")); !os.IsNotExist(err) {
		t.Errorf("traffic dir created without a traffic store: %v", err)
	}
	// RestoreTraffic with a nil store is a no-op.
	m.RestoreTraffic(ctx)
}
