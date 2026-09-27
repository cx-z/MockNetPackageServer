package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// 4.22: the runtime traffic ID index must stay consistent with the slices
// across every mutation — rolling-window trim unindexes the oldest entries,
// clear unindexes the whole session, session end flips active entries to
// retained (owner resolves from the end-time snapshot), and the janitor
// unindexes purged retained entries.
func TestCaptureManager_TrafficIndexConsistency(t *testing.T) {
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

	// Fill the window exactly, then overflow by one entry: the oldest entry is
	// trimmed from the slice AND must leave the index.
	base := time.Now()
	first := make([]*capture.TrafficEntry, store.MaxSessionTrafficEntries)
	for i := range first {
		first[i] = trafficEntry("GET", "http://example.com/first", base.Add(time.Duration(i)*time.Millisecond))
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, first); err != nil {
		t.Fatalf("UploadTraffic(first) = %v", err)
	}
	full, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil || len(full) != store.MaxSessionTrafficEntries {
		t.Fatalf("ListSessionTraffic(full) = %d entries, err %v", len(full), err)
	}
	oldestID := full[0].ID

	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("GET", "http://example.com/new", base)}); err != nil {
		t.Fatalf("UploadTraffic(new) = %v", err)
	}
	if _, err := m.GetTraffic(ctx, oldestID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(trimmed oldest) = %v, want ErrNotFound (index must follow the window trim)", err)
	}
	window, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	if err != nil || len(window) != store.MaxSessionTrafficEntries {
		t.Fatalf("ListSessionTraffic(window) = %d entries, err %v", len(window), err)
	}
	survivorID := window[0].ID
	if _, err := m.GetTraffic(ctx, survivorID); err != nil {
		t.Errorf("GetTraffic(survivor) = %v, want present", err)
	}

	// Active entry: owner resolves via the session record.
	if app, did, err := m.GetTrafficWithOwner(ctx, survivorID); err != nil || app != "app" || did != "d1" {
		t.Errorf("GetTrafficWithOwner(active) = (%q, %q, %v), want (app, d1, nil)", app, did, err)
	}

	// Clear unindexes the whole session.
	if err := m.ClearSessionTraffic(ctx, s.ID); err != nil {
		t.Fatalf("ClearSessionTraffic() = %v", err)
	}
	if _, err := m.GetTraffic(ctx, survivorID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(after clear) = %v, want ErrNotFound (index must follow clear)", err)
	}

	// Re-upload one entry and end the session: the entry moves to retained and
	// the index flips — still resolvable, owner from the end-time snapshot.
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{trafficEntry("POST", "http://example.com/retain", base)}); err != nil {
		t.Fatalf("UploadTraffic(retain) = %v", err)
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0)
	retainedID := list[0].ID
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if _, err := m.GetTraffic(ctx, retainedID); err != nil {
		t.Errorf("GetTraffic(retained) = %v, want present", err)
	}
	if app, did, err := m.GetTrafficWithOwner(ctx, retainedID); err != nil || app != "app" || did != "d1" {
		t.Errorf("GetTrafficWithOwner(retained) = (%q, %q, %v), want (app, d1, nil) from snapshot", app, did, err)
	}

	// Janitor purge unindexes the retained entry.
	time.Sleep(80 * time.Millisecond)
	m.PurgeExpiredRetainedTraffic(ctx)
	if _, err := m.GetTraffic(ctx, retainedID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTraffic(after purge) = %v, want ErrNotFound (index must follow purge)", err)
	}
	if _, err := m.CreateShare(ctx, retainedID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreateShare(after purge) = %v, want ErrNotFound", err)
	}
}
