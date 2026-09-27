package file

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// newCaptureManager returns a CaptureManager over a fresh test store with a
// short heartbeat timeout so timeout behavior can be exercised quickly.
func newCaptureManager(t *testing.T, timeout time.Duration) (*store.CaptureManager, *FileStore) {
	t.Helper()
	fs := newTestStore(t)
	cfg := store.DefaultCaptureConfig()
	if timeout > 0 {
		cfg.HeartbeatTimeout = timeout
	}
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), cfg)
	t.Cleanup(m.Stop)
	return m, fs
}

func TestCaptureManager_RegisterDevice_Upsert(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	d := &capture.Device{App: "com.example.app", Did: "dev-001", OSVersion: "17.5", SDKVersion: "0.1.0"}
	registered, err := m.RegisterDevice(ctx, d)
	if err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	if registered.RegisteredAt.IsZero() {
		t.Error("RegisteredAt not set on first registration")
	}
	firstRegisteredAt := registered.RegisteredAt

	// Re-register (e.g. app relaunch): same (App, Did), idempotent upsert.
	time.Sleep(5 * time.Millisecond)
	d2 := &capture.Device{App: "com.example.app", Did: "dev-001", OSVersion: "17.6", SDKVersion: "0.1.0"}
	registered2, err := m.RegisterDevice(ctx, d2)
	if err != nil {
		t.Fatalf("RegisterDevice(again) = %v", err)
	}
	if !registered2.RegisteredAt.Equal(firstRegisteredAt) {
		t.Errorf("RegisteredAt changed on re-register: %v -> %v", firstRegisteredAt, registered2.RegisteredAt)
	}
	if registered2.OSVersion != "17.6" {
		t.Errorf("OSVersion not refreshed on re-register: %q", registered2.OSVersion)
	}
	if !registered2.LastSeenAt.After(registered.LastSeenAt) {
		t.Error("LastSeenAt not refreshed on re-register")
	}
}

func TestCaptureManager_Heartbeat(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// Heartbeat for unknown device -> ErrDeviceNotRegistered.
	if _, _, err := m.Heartbeat(ctx, "app", "unknown"); !errors.Is(err, store.ErrDeviceNotRegistered) {
		t.Errorf("Heartbeat(unknown) = %v, want ErrDeviceNotRegistered", err)
	}

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	d, session, err := m.Heartbeat(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("Heartbeat() = %v", err)
	}
	if d == nil || session != nil {
		t.Errorf("Heartbeat() = device %v, session %v; want device set, session nil", d, session)
	}

	// After activating a session, heartbeat returns the active session.
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	_, session, err = m.Heartbeat(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("Heartbeat() = %v", err)
	}
	if session == nil || session.ID != s.ID {
		t.Errorf("Heartbeat() session = %v, want %v", session, s.ID)
	}
}

func TestCaptureManager_ActivateSession(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// Unknown device.
	if _, _, err := m.ActivateSession(ctx, "app", "nope"); !errors.Is(err, store.ErrDeviceNotRegistered) {
		t.Errorf("ActivateSession(unknown) = %v, want ErrDeviceNotRegistered", err)
	}

	// Offline device (last heartbeat beyond timeout) is rejected. RegisterDevice
	// always refreshes LastSeenAt, so construct the stale device directly in the
	// store.
	m2, fs2 := newCaptureManager(t, 50*time.Millisecond)
	if err := fs2.Devices().Create(ctx, &capture.Device{
		App: "app", Did: "stale",
		RegisteredAt: time.Now().Add(-time.Hour),
		LastSeenAt:   time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Create(stale) = %v", err)
	}
	if _, _, err := m2.ActivateSession(ctx, "app", "stale"); !errors.Is(err, store.ErrDeviceOffline) {
		t.Errorf("ActivateSession(offline) = %v, want ErrDeviceOffline", err)
	}

	// Normal activation.
	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s1, created, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil || !created {
		t.Fatalf("ActivateSession() = %v, created=%v; want ok, created=true", err, created)
	}
	if s1.Status != capture.SessionStatusCapturing || s1.App != "app" || s1.Did != "d1" {
		t.Errorf("ActivateSession() session = %+v", s1)
	}

	// Second activation returns the same session (single session per device).
	s2, created, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil || created || s2.ID != s1.ID {
		t.Errorf("ActivateSession(again) = id %v, created=%v, err=%v; want same id, created=false", s2.ID, created, err)
	}
}

func TestCaptureManager_EndSession(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if err := m.EndSession(ctx, "nonexistent"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("EndSession(unknown) = %v, want ErrSessionNotFound", err)
	}

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	// M9 (会话结束即删): the session record is deleted on end.
	if _, err := m.GetSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("GetSession() after end = %v, want ErrSessionNotFound (M9: 结束即删)", err)
	}

	// Ending a deleted session reports not found (Web treats 404 as success).
	if err := m.EndSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("EndSession(again) = %v, want ErrSessionNotFound (record deleted)", err)
	}

	// Device list reflects no active session after end.
	views, err := m.ListDevices(ctx, nil)
	if err != nil || len(views) != 1 {
		t.Fatalf("ListDevices() = %d, %v", len(views), err)
	}
	if views[0].CurrentSession != nil || views[0].Status != capture.DeviceStatusIdle {
		t.Errorf("ListDevices() = %+v, want no session + idle", views[0])
	}
}

func TestCaptureManager_ViewerLifecycle_LastViewerEndsSession(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	// Register two viewers.
	l1, err := m.RegisterViewer(ctx, s.ID, "viewer-1", "page-a")
	if err != nil {
		t.Fatalf("RegisterViewer(1) = %v", err)
	}
	if l1.TTLSeconds <= 0 {
		t.Errorf("lease TTL = %d, want > 0", l1.TTLSeconds)
	}
	if _, err := m.RegisterViewer(ctx, s.ID, "viewer-2", "page-b"); err != nil {
		t.Fatalf("RegisterViewer(2) = %v", err)
	}

	got, _ := m.GetSession(ctx, s.ID)
	if got.ViewerCount != 2 {
		t.Errorf("ViewerCount = %d, want 2", got.ViewerCount)
	}

	// Release one viewer: session stays capturing.
	if err := m.ReleaseViewer(ctx, s.ID, "viewer-1"); err != nil {
		t.Fatalf("ReleaseViewer(1) = %v", err)
	}
	got, _ = m.GetSession(ctx, s.ID)
	if got.ViewerCount != 1 || got.Status != capture.SessionStatusCapturing {
		t.Errorf("after release: count=%d status=%q; want 1 capturing", got.ViewerCount, got.Status)
	}

	// Renew the second viewer (idempotent lease refresh).
	if _, err := m.RegisterViewer(ctx, s.ID, "viewer-2", "page-b"); err != nil {
		t.Fatalf("RegisterViewer(renew) = %v", err)
	}
	got, _ = m.GetSession(ctx, s.ID)
	if got.ViewerCount != 1 {
		t.Errorf("renew changed ViewerCount = %d, want 1", got.ViewerCount)
	}

	// Release the last viewer: session ends and its record is deleted (M9).
	if err := m.ReleaseViewer(ctx, s.ID, "viewer-2"); err != nil {
		t.Fatalf("ReleaseViewer(2) = %v", err)
	}
	if _, err := m.GetSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("GetSession() after last release = %v, want ErrSessionNotFound (M9: 结束即删)", err)
	}

	// Releasing an unknown viewer is idempotent.
	if err := m.ReleaseViewer(ctx, s.ID, "ghost"); err != nil {
		t.Errorf("ReleaseViewer(ghost) = %v, want nil", err)
	}
}

func TestCaptureManager_HeartbeatTimeout_EndsSession(t *testing.T) {
	// Health check must end the session when the device heartbeat times out.
	m, _ := newCaptureManager(t, 300*time.Millisecond)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	m.StartHealthCheck(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.GetSession(ctx, s.ID); errors.Is(err, store.ErrSessionNotFound) {
			return // session ended (M9: record deleted) by health check
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session not ended by health check after device heartbeat timeout")
}

func TestCaptureManager_ViewerLeaseExpiry_EndsSession(t *testing.T) {
	// Expired viewer leases (no page-close event) must be garbage-collected
	// and end the session when the last lease expires.
	cfg := store.DefaultCaptureConfig()
	cfg.HeartbeatTimeout = 5 * time.Second
	cfg.ViewerTTL = 200 * time.Millisecond
	fs := newTestStore(t)
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), cfg)
	t.Cleanup(m.Stop)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.RegisterViewer(ctx, s.ID, "viewer-1", "page"); err != nil {
		t.Fatalf("RegisterViewer() = %v", err)
	}

	m.StartHealthCheck(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.GetSession(ctx, s.ID); errors.Is(err, store.ErrSessionNotFound) {
			return // session ended (M9: record deleted) after viewer lease expiry
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session not ended after viewer lease expiry")
}

func TestCaptureManager_MultiDeviceIsolation(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	for _, d := range []*capture.Device{
		{App: "app-a", Did: "dev-1"},
		{App: "app-a", Did: "dev-2"},
		{App: "app-b", Did: "dev-1"}, // same did, different app
	} {
		if _, err := m.RegisterDevice(ctx, d); err != nil {
			t.Fatalf("RegisterDevice(%s/%s) = %v", d.App, d.Did, err)
		}
	}

	// Activate only app-a/dev-1.
	if _, _, err := m.ActivateSession(ctx, "app-a", "dev-1"); err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	views, err := m.ListDevices(ctx, nil)
	if err != nil {
		t.Fatalf("ListDevices() = %v", err)
	}
	byKey := map[string]*capture.DeviceView{}
	for _, v := range views {
		byKey[v.App+"/"+v.Did] = v
	}

	if got := byKey["app-a/dev-1"]; got == nil || got.Status != capture.DeviceStatusCapturing || got.CurrentSession == nil {
		t.Errorf("app-a/dev-1 = %+v, want capturing + session", got)
	}
	if got := byKey["app-a/dev-2"]; got == nil || got.Status != capture.DeviceStatusIdle || got.CurrentSession != nil {
		t.Errorf("app-a/dev-2 = %+v, want idle + no session", got)
	}
	if got := byKey["app-b/dev-1"]; got == nil || got.Status != capture.DeviceStatusIdle || got.CurrentSession != nil {
		t.Errorf("app-b/dev-1 = %+v, want idle + no session (cross-app isolation)", got)
	}

	// Heartbeat for app-b/dev-1 must not see app-a/dev-1's session.
	_, session, err := m.Heartbeat(ctx, "app-b", "dev-1")
	if err != nil || session != nil {
		t.Errorf("Heartbeat(app-b/dev-1) session = %v, %v; want nil (isolation)", session, err)
	}
	_, session, err = m.Heartbeat(ctx, "app-a", "dev-1")
	if err != nil || session == nil {
		t.Errorf("Heartbeat(app-a/dev-1) session = %v, %v; want session", session, err)
	}
}

// ---- Traffic (M2.2) ----

// trafficEntry returns a valid TrafficEntry for tests.
func trafficEntry(method, url string, ts time.Time) *capture.TrafficEntry {
	return &capture.TrafficEntry{Method: method, URL: url, Timestamp: ts, DurationMs: 12}
}

func TestCaptureManager_UploadTraffic_Validation(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	// Unknown session -> ErrSessionNotFound.
	if _, err := m.UploadTraffic(ctx, "app", "d1", "no-such-session",
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("UploadTraffic(unknown session) = %v, want ErrSessionNotFound", err)
	}

	// Cross-device upload (isolation): a session owned by (app, d1) must not
	// accept traffic tagged with another device.
	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d2"}); err != nil {
		t.Fatalf("RegisterDevice(d2) = %v", err)
	}
	s2, _, err := m.ActivateSession(ctx, "app", "d2")
	if err != nil {
		t.Fatalf("ActivateSession(d2) = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s2.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("UploadTraffic(cross-device) = %v, want ErrSessionNotFound", err)
	}

	// Ended session (M9: record deleted) -> ErrSessionNotFound.
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("UploadTraffic(ended session) = %v, want ErrSessionNotFound (M9: 结束即删)", err)
	}
}

func TestCaptureManager_UploadTraffic_AcceptAndCount(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	now := time.Now()
	entries := []*capture.TrafficEntry{
		trafficEntry("GET", "http://example.com/a?x=1", now), // kept
		nil, // dropped: nil
		{Method: "", URL: "http://x/b", Timestamp: now},                 // dropped: empty method
		{Method: "POST", URL: "", Timestamp: now},                       // dropped: empty url
		{Method: "PUT", URL: "http://x/c", Timestamp: time.Time{}},      // dropped: zero timestamp
		trafficEntry("DELETE", "http://x/d", now.Add(time.Millisecond)), // kept
	}
	count, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries)
	if err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (2 of 6 kept)", count)
	}

	// Session RequestCount persisted.
	got, err := m.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession() = %v", err)
	}
	if got.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", got.RequestCount)
	}

	// Server-generated ID + session binding + arrival order preserved.
	list, total, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(list))
	}
	if list[0].ID == "" || list[0].SessionID != s.ID {
		t.Errorf("entry id/session = %q/%q, want generated id + %q", list[0].ID, list[0].SessionID, s.ID)
	}
	if list[0].Method != "GET" || list[1].Method != "DELETE" {
		t.Errorf("order = %q,%q; want GET,DELETE", list[0].Method, list[1].Method)
	}

	// Returned entries are copies: mutating them must not touch storage.
	list[0].Method = "HACKED"
	again, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if again[0].Method != "GET" {
		t.Errorf("stored entry mutated through returned copy: %q", again[0].Method)
	}

	// GetTraffic by server-generated ID.
	first, err := m.GetTraffic(ctx, again[0].ID)
	if err != nil || first.URL != "http://example.com/a?x=1" {
		t.Errorf("GetTraffic() = %+v, %v", first, err)
	}
	if _, err := m.GetTraffic(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(unknown) = %v, want ErrNotFound", err)
	}
}

// TestCaptureManager_UploadTraffic_RollingWindow (4.10): once a session
// exceeds MaxSessionTrafficEntries, the oldest entries are dropped and only
// the most recent cap are kept, while RequestCount still counts every
// accepted upload.
func TestCaptureManager_UploadTraffic_RollingWindow(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	base := time.Now()
	entries := make([]*capture.TrafficEntry, store.MaxSessionTrafficEntries+10)
	for i := range entries {
		entries[i] = trafficEntry("GET", "http://example.com/t", base.Add(time.Duration(i)*time.Millisecond))
	}
	count, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries)
	if err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if count != len(entries) {
		t.Fatalf("count = %d, want %d (all accepted)", count, len(entries))
	}

	list, total, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	if total != store.MaxSessionTrafficEntries || len(list) != store.MaxSessionTrafficEntries {
		t.Fatalf("total=%d len=%d, want %d/%d", total, len(list), store.MaxSessionTrafficEntries, store.MaxSessionTrafficEntries)
	}
	// The dropped 10 are the OLDEST; the kept window starts at index 10 of the
	// upload and the newest entry is preserved.
	if list[0].Timestamp != entries[10].Timestamp {
		t.Errorf("oldest kept = %v, want upload entry[10] %v", list[0].Timestamp, entries[10].Timestamp)
	}
	if list[len(list)-1].Timestamp != entries[len(entries)-1].Timestamp {
		t.Errorf("newest kept = %v, want upload entry[last] %v", list[len(list)-1].Timestamp, entries[len(entries)-1].Timestamp)
	}

	// RequestCount counts every accepted upload, not just the kept window.
	got, err := m.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession() = %v", err)
	}
	if got.RequestCount != len(entries) {
		t.Errorf("RequestCount = %d, want %d", got.RequestCount, len(entries))
	}

	// A second overflow batch keeps rolling: the first batch's entries that
	// survived must now be gone as well (window keeps moving forward).
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries[:1]); err != nil {
		t.Fatalf("second UploadTraffic() = %v", err)
	}
	list, total, _ = m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if total != store.MaxSessionTrafficEntries {
		t.Fatalf("total after second batch = %d, want %d", total, store.MaxSessionTrafficEntries)
	}
	if list[len(list)-1].Timestamp != entries[0].Timestamp {
		t.Errorf("newest after second batch = %v, want entry[0] %v", list[len(list)-1].Timestamp, entries[0].Timestamp)
	}
}

func TestCaptureManager_ListSessionTraffic_PagingAndClear(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	now := time.Now()
	entries := make([]*capture.TrafficEntry, 5)
	for i := range entries {
		entries[i] = trafficEntry("GET", "http://x/"+string(rune('a'+i)), now.Add(time.Duration(i)*time.Millisecond))
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}

	// Paging: limit 2, offset 1 -> entries 1..2 of 5.
	list, total, err := m.ListSessionTraffic(ctx, s.ID, 2, 1)
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	if total != 5 || len(list) != 2 {
		t.Fatalf("total=%d len=%d, want 5/2", total, len(list))
	}
	if list[0].URL != "http://x/b" || list[1].URL != "http://x/c" {
		t.Errorf("paged = %q,%q; want b,c", list[0].URL, list[1].URL)
	}

	// limit 0 = no limit.
	list, total, _ = m.ListSessionTraffic(ctx, s.ID, 0, 3)
	if len(list) != 2 || total != 5 {
		t.Errorf("limit=0 offset=3: len=%d total=%d; want 2/5", len(list), total)
	}

	// offset beyond total -> empty.
	list, total, _ = m.ListSessionTraffic(ctx, s.ID, 10, 99)
	if len(list) != 0 || total != 5 {
		t.Errorf("offset beyond: len=%d total=%d; want 0/5", len(list), total)
	}

	// Unknown session.
	if _, _, err := m.ListSessionTraffic(ctx, "no-such-session", 0, 0); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("ListSessionTraffic(unknown) = %v, want ErrSessionNotFound", err)
	}

	// Ended session (M9: record deleted): traffic/entry/session are gone.
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if _, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("ListSessionTraffic(ended) = %v, want ErrSessionNotFound (M9: 结束即删)", err)
	}
	if _, err := m.GetSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("GetSession(after end) = %v, want ErrSessionNotFound", err)
	}
	if _, err := m.GetTraffic(ctx, "any-old-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(after end) = %v, want ErrNotFound", err)
	}
}

func TestCaptureManager_Traffic_SessionIsolation(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	for _, d := range []*capture.Device{{App: "app", Did: "d1"}, {App: "app", Did: "d2"}} {
		if _, err := m.RegisterDevice(ctx, d); err != nil {
			t.Fatalf("RegisterDevice(%s) = %v", d.Did, err)
		}
	}
	s1, _, _ := m.ActivateSession(ctx, "app", "d1")
	s2, _, _ := m.ActivateSession(ctx, "app", "d2")
	if _, err := m.UploadTraffic(ctx, "app", "d1", s1.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/one", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic(s1) = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d2", s2.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/two", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic(s2) = %v", err)
	}

	list1, total1, _ := m.ListSessionTraffic(ctx, s1.ID, 0, 0)
	if total1 != 1 || len(list1) != 1 || list1[0].URL != "http://x/one" {
		t.Errorf("session1 traffic = %d/%d (%q); want 1/1 one", len(list1), total1, list1[0].URL)
	}
	list2, total2, _ := m.ListSessionTraffic(ctx, s2.ID, 0, 0)
	if total2 != 1 || len(list2) != 1 || list2[0].URL != "http://x/two" {
		t.Errorf("session2 traffic = %d/%d (%q); want 1/1 two", len(list2), total2, list2[0].URL)
	}
}

func TestCaptureManager_HeartbeatTimeout_ClearsTraffic(t *testing.T) {
	// Health check ends the session on heartbeat timeout; the session's
	// temporary traffic must be cleared along with it.
	m, _ := newCaptureManager(t, 300*time.Millisecond)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}

	m.StartHealthCheck(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0); errors.Is(err, store.ErrSessionNotFound) {
			return // session ended (M9: record deleted; ended sessions are never listed)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("traffic not cleared after heartbeat-timeout session end")
}

func TestCaptureManager_ShareAfterSessionEnd(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessionTraffic() = %d, %v; want 1 entry", len(list), err)
	}
	id := list[0].ID

	// M9: ending the session deletes its record...
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if _, err := m.GetSession(ctx, s.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("GetSession(after end) = %v, want ErrSessionNotFound (M9: 结束即删)", err)
	}

	// M8.6: retained traffic is still resolvable by ID with its owning device.
	e, err := m.GetTraffic(ctx, id)
	if err != nil || e.ID != id || e.URL != "http://x/a" {
		t.Fatalf("GetTraffic(retained) = %+v, %v; want entry %q", e, err, id)
	}
	app, did, err := m.GetTrafficWithOwner(ctx, id)
	if err != nil || app != "app" || did != "d1" {
		t.Errorf("GetTrafficWithOwner(retained) = %q/%q, %v; want app/d1", app, did, err)
	}
	if _, _, err := m.GetTrafficWithOwner(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTrafficWithOwner(unknown) = %v, want ErrNotFound", err)
	}

	// Share creation now succeeds after disconnect (previously 404).
	snap, err := m.CreateShare(ctx, id)
	if err != nil {
		t.Fatalf("CreateShare(after end) = %v", err)
	}
	if snap.Entry == nil || snap.Entry.ID != id || snap.Entry.URL != "http://x/a" {
		t.Errorf("share entry = %+v, want deep copy of %q", snap.Entry, id)
	}
	if snap.Entry.SessionID != "" {
		t.Errorf("share entry SessionID = %q, want empty (no internal session linkage)", snap.Entry.SessionID)
	}
	got, err := m.GetShare(ctx, snap.ShareID)
	if err != nil || got.Entry.URL != "http://x/a" {
		t.Errorf("GetShare() = %+v, %v", got, err)
	}

	// M9 list semantics unchanged: the ended session is not listable.
	if _, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("ListSessionTraffic(ended) = %v, want ErrSessionNotFound", err)
	}
}

func TestCaptureManager_SharePersistsAcrossRestart(t *testing.T) {
	// 4.8 回归：分享快照落盘持久化——服务端重启后链接仍有效（7 天 TTL 是对
	// 用户的承诺，不应随进程内存一起消失）。
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

	fs := newFS()
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessionTraffic() = %d, %v; want 1 entry", len(list), err)
	}
	snap, err := m.CreateShare(ctx, list[0].ID)
	if err != nil {
		t.Fatalf("CreateShare() = %v", err)
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

	got, err := m2.GetShare(ctx, snap.ShareID)
	if err != nil {
		t.Fatalf("share lost after restart: %v", err)
	}
	if got.Entry == nil || got.Entry.URL != "http://x/a" || got.Entry.Method != "GET" {
		t.Fatalf("share entry corrupted after restart: %+v", got.Entry)
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Fatalf("share TTL not preserved: expiresAt=%v", got.ExpiresAt)
	}
}

// TestCaptureManager_DeleteRetainedTraffic (4.11): after the session ends, its
// entries move to the retained store; deleting one of them by ID must succeed
// (the owning session record is gone, but the row is still the device's data)
// instead of 404ing at the session-access check.
func TestCaptureManager_DeleteRetainedTraffic(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	for i, u := range []string{"http://x/a", "http://x/b"} {
		if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
			[]*capture.TrafficEntry{trafficEntry("GET", u, time.Now().Add(time.Duration(i)*time.Millisecond))}); err != nil {
			t.Fatalf("UploadTraffic() = %v", err)
		}
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	delID, keepID := list[0].ID, list[1].ID

	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	// Both entries are now retained (session record deleted, M9).
	if _, err := m.GetTraffic(ctx, delID); err != nil {
		t.Fatalf("GetTraffic(retained, before delete) = %v", err)
	}

	if err := m.DeleteTraffic(ctx, delID); err != nil {
		t.Fatalf("DeleteTraffic(retained) = %v, want nil (4.11)", err)
	}
	if _, err := m.GetTraffic(ctx, delID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(deleted retained) = %v, want ErrNotFound", err)
	}
	// The sibling retained entry is untouched.
	if _, err := m.GetTraffic(ctx, keepID); err != nil {
		t.Errorf("GetTraffic(sibling retained) = %v, want still present", err)
	}
	// Deleting the last retained entry cleans the retained session slot.
	if err := m.DeleteTraffic(ctx, keepID); err != nil {
		t.Fatalf("DeleteTraffic(last retained) = %v", err)
	}
	if _, err := m.GetTraffic(ctx, keepID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(last deleted) = %v, want ErrNotFound", err)
	}
	// Unknown ID stays a clean 404.
	if err := m.DeleteTraffic(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteTraffic(unknown) = %v, want ErrNotFound", err)
	}
}

func TestCaptureManager_RetainedTraffic_Expires(t *testing.T) {
	fs := newTestStore(t)
	cfg := store.DefaultCaptureConfig()
	cfg.RetainedTrafficTTL = 50 * time.Millisecond
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), cfg)
	t.Cleanup(m.Stop)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://x/a", time.Now())}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	id := list[0].ID
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}

	// Within the retention window the entry stays shareable.
	if _, err := m.GetTraffic(ctx, id); err != nil {
		t.Fatalf("GetTraffic(within TTL) = %v", err)
	}

	// After the window passes, the janitor purges it and sharing fails again.
	time.Sleep(200 * time.Millisecond)
	m.PurgeExpiredRetainedTraffic(ctx)
	if _, err := m.GetTraffic(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(after purge) = %v, want ErrNotFound", err)
	}
	if _, err := m.CreateShare(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreateShare(after purge) = %v, want ErrNotFound", err)
	}
}
