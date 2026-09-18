package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

func now() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }

var sessionEndPtr = func() *time.Time { t := now().Add(time.Minute); return &t }()

func TestDeviceStore_CreateGetUpdateDelete(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()
	devices := fs.Devices()

	d := &capture.Device{
		App:          "com.example.app",
		Did:          "dev-001",
		Platform:     capture.PlatformIOS,
		OSVersion:    "17.5",
		SDKVersion:   "0.1.0",
		LastSeenAt:   now(),
		RegisteredAt: now(),
	}

	if err := devices.Create(ctx, d); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := devices.Get(ctx, d.App, d.Did)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got.App != d.App || got.Did != d.Did {
		t.Errorf("Get() = %+v, want app=%s did=%s", got, d.App, d.Did)
	}

	// Update refreshes lastSeen.
	d.LastSeenAt = now().Add(30 * time.Second)
	if err := devices.Update(ctx, d); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	got, _ = devices.Get(ctx, d.App, d.Did)
	if !got.LastSeenAt.Equal(d.LastSeenAt) {
		t.Errorf("Update() lastSeen = %v, want %v", got.LastSeenAt, d.LastSeenAt)
	}

	if err := devices.Delete(ctx, d.App, d.Did); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, err := devices.Get(ctx, d.App, d.Did); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after Delete = %v, want ErrNotFound", err)
	}
}

func TestDeviceStore_AppDidUnique(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()
	devices := fs.Devices()

	d1 := &capture.Device{App: "com.example.app", Did: "dev-001", RegisteredAt: now()}
	if err := devices.Create(ctx, d1); err != nil {
		t.Fatalf("Create(d1) = %v", err)
	}

	// Same (App, Did) must be rejected.
	dup := &capture.Device{App: "com.example.app", Did: "dev-001", RegisteredAt: now()}
	if err := devices.Create(ctx, dup); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("Create(dup) = %v, want ErrAlreadyExists", err)
	}

	// Same did under a different app is a DIFFERENT device (隔离强制).
	d2 := &capture.Device{App: "com.example.other", Did: "dev-001", RegisteredAt: now()}
	if err := devices.Create(ctx, d2); err != nil {
		t.Errorf("Create(same did, other app) = %v, want ok (device isolation)", err)
	}

	// Same app with a different did is a different device.
	d3 := &capture.Device{App: "com.example.app", Did: "dev-002", RegisteredAt: now()}
	if err := devices.Create(ctx, d3); err != nil {
		t.Errorf("Create(same app, other did) = %v, want ok", err)
	}

	n, err := devices.Count(ctx)
	if err != nil || n != 3 {
		t.Errorf("Count() = %d, %v; want 3", n, err)
	}
}

func TestDeviceStore_ListFilterByApp(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()
	devices := fs.Devices()

	for _, d := range []*capture.Device{
		{App: "app-a", Did: "d1", RegisteredAt: now()},
		{App: "app-a", Did: "d2", RegisteredAt: now()},
		{App: "app-b", Did: "d1", RegisteredAt: now()},
	} {
		if err := devices.Create(ctx, d); err != nil {
			t.Fatalf("Create(%s/%s) = %v", d.App, d.Did, err)
		}
	}

	all, err := devices.List(ctx, nil)
	if err != nil || len(all) != 3 {
		t.Errorf("List(nil) = %d items, %v; want 3", len(all), err)
	}

	filtered, err := devices.List(ctx, &store.DeviceFilter{App: "app-a"})
	if err != nil || len(filtered) != 2 {
		t.Errorf("List(app-a) = %d items, %v; want 2", len(filtered), err)
	}
}

func TestCaptureSessionStore_Lifecycle(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()
	sessions := fs.CaptureSessions()

	s := &capture.CaptureSession{
		ID:        "sess-001",
		App:       "com.example.app",
		Did:       "dev-001",
		Status:    capture.SessionStatusCapturing,
		StartedAt: now(),
	}
	if err := sessions.Create(ctx, s); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	// Update session summary (e.g. request count, status -> ended).
	s.RequestCount = 42
	s.End(now().Add(5 * time.Minute))
	if err := sessions.Update(ctx, s); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	got, err := sessions.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got.Status != capture.SessionStatusEnded || got.RequestCount != 42 {
		t.Errorf("Get() = %+v, want ended + count 42", got)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(now().Add(5*time.Minute)) {
		t.Errorf("EndedAt = %v, want %v", got.EndedAt, now().Add(5*time.Minute))
	}

	if err := sessions.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, err := sessions.Get(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after Delete = %v, want ErrNotFound", err)
	}
}

func TestCaptureSessionStore_ListSortedMostRecentFirst(t *testing.T) {
	fs := newTestStore(t)
	ctx := context.Background()
	sessions := fs.CaptureSessions()

	old := &capture.CaptureSession{ID: "s-old", App: "a", Did: "d", Status: capture.SessionStatusEnded, StartedAt: now().Add(-2 * time.Hour)}
	mid := &capture.CaptureSession{ID: "s-mid", App: "a", Did: "d", Status: capture.SessionStatusEnded, StartedAt: now().Add(-1 * time.Hour)}
	newer := &capture.CaptureSession{ID: "s-new", App: "a", Did: "d", Status: capture.SessionStatusCapturing, StartedAt: now()}
	for _, s := range []*capture.CaptureSession{mid, newer, old} {
		if err := sessions.Create(ctx, s); err != nil {
			t.Fatalf("Create() = %v", err)
		}
	}

	all, err := sessions.List(ctx, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("List(nil) = %d items, %v; want 3", len(all), err)
	}
	if all[0].ID != "s-new" || all[1].ID != "s-mid" || all[2].ID != "s-old" {
		t.Errorf("List() order = [%s %s %s], want [s-new s-mid s-old]", all[0].ID, all[1].ID, all[2].ID)
	}

	// Filter by did isolates sessions.
	other := &capture.CaptureSession{ID: "s-other", App: "a", Did: "d2", Status: capture.SessionStatusEnded, StartedAt: now()}
	if err := sessions.Create(ctx, other); err != nil {
		t.Fatalf("Create(other) = %v", err)
	}
	did := "d"
	filtered, err := sessions.List(ctx, &store.SessionFilter{Did: &did})
	if err != nil || len(filtered) != 3 {
		t.Errorf("List(did=d) = %d items, %v; want 3", len(filtered), err)
	}

	// Filter by status.
	capturing := capture.SessionStatusCapturing
	active, err := sessions.List(ctx, &store.SessionFilter{Status: &capturing})
	if err != nil || len(active) != 1 || active[0].ID != "s-new" {
		t.Errorf("List(capturing) = %+v, %v; want [s-new]", active, err)
	}
}

func TestCaptureStore_PersistsAcrossReopen(t *testing.T) {
	// Devices and capture session summaries must survive a store reopen
	// (the device list stays visible after a server restart).
	dir := t.TempDir()
	ctx := context.Background()

	fs1 := New(store.Config{
		DataDir:   dir,
		ConfigDir: dir + "/config",
		CacheDir:  dir + "/cache",
		StateDir:  dir + "/state",
	})
	if err := fs1.Open(ctx); err != nil {
		t.Fatalf("Open(1) = %v", err)
	}
	if err := fs1.Devices().Create(ctx, &capture.Device{App: "a", Did: "d", RegisteredAt: now()}); err != nil {
		t.Fatalf("Create device = %v", err)
	}
	if err := fs1.CaptureSessions().Create(ctx, &capture.CaptureSession{
		ID: "s", App: "a", Did: "d", Status: capture.SessionStatusEnded, StartedAt: now(), EndedAt: sessionEndPtr,
	}); err != nil {
		t.Fatalf("Create session = %v", err)
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close(1) = %v", err)
	}

	fs2 := New(store.Config{
		DataDir:   dir,
		ConfigDir: dir + "/config",
		CacheDir:  dir + "/cache",
		StateDir:  dir + "/state",
	})
	if err := fs2.Open(ctx); err != nil {
		t.Fatalf("Open(2) = %v", err)
	}
	defer func() { _ = fs2.Close() }()

	if n, _ := fs2.Devices().Count(ctx); n != 1 {
		t.Errorf("device count after reopen = %d, want 1", n)
	}
	if _, err := fs2.Devices().Get(ctx, "a", "d"); err != nil {
		t.Errorf("device Get after reopen = %v, want ok", err)
	}
	if s, err := fs2.CaptureSessions().Get(ctx, "s"); err != nil || s.Status != capture.SessionStatusEnded {
		t.Errorf("session Get after reopen = %+v, %v; want ended", s, err)
	}
}
