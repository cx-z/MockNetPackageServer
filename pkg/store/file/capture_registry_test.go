package file

import (
	"context"
	"errors"
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
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), cfg)
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
	got, err := m.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession() = %v", err)
	}
	if got.Status != capture.SessionStatusEnded || got.EndedAt == nil {
		t.Errorf("GetSession() = %+v, want ended with EndedAt", got)
	}

	// Idempotent.
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Errorf("EndSession(again) = %v, want nil (idempotent)", err)
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

	// Release the last viewer: session ends.
	if err := m.ReleaseViewer(ctx, s.ID, "viewer-2"); err != nil {
		t.Fatalf("ReleaseViewer(2) = %v", err)
	}
	got, _ = m.GetSession(ctx, s.ID)
	if got.Status != capture.SessionStatusEnded || got.ViewerCount != 0 {
		t.Errorf("after last release: status=%q count=%d; want ended 0", got.Status, got.ViewerCount)
	}
	if got.EndedAt == nil {
		t.Error("EndedAt not set after last viewer release")
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
		got, err := m.GetSession(ctx, s.ID)
		if err == nil && got.Status == capture.SessionStatusEnded {
			return // session ended by health check
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
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), cfg)
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
		got, err := m.GetSession(ctx, s.ID)
		if err == nil && got.Status == capture.SessionStatusEnded {
			if got.ViewerCount != 0 {
				t.Errorf("ViewerCount = %d, want 0 after expiry", got.ViewerCount)
			}
			return
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
