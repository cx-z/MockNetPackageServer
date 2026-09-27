package file_test

// Persistence-policy tests for the P1-4.4 fix: high-frequency updates
// (heartbeat LastSeenAt, per-batch RequestCount) must be memory-only — they
// must never dirty the store and thus never rewrite data.json on disk.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/getmockd/mockd/pkg/store/file"
)

// persistedData mirrors the subset of file.storeData the assertions read back.
type persistedData struct {
	Devices         []*capture.Device         `json:"devices"`
	CaptureSessions []*capture.CaptureSession `json:"captureSessions"`
}

// waitForDataFile polls until data.json exists (the store's debounced save
// fires ~500ms after a dirty-marking call) and returns its parsed content.
func waitForDataFile(t *testing.T, dir string) persistedData {
	t.Helper()
	path := filepath.Join(dir, "data.json")
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			var got persistedData
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("parse data.json: %v", err)
			}
			return got
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read data.json: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("data.json was never written")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestUpdateLastSeenIsMemoryOnly proves a heartbeat does not touch disk:
// after the initial create is persisted, an UpdateLastSeen with a new time is
// never written back to data.json.
func TestUpdateLastSeenIsMemoryOnly(t *testing.T) {
	dir := t.TempDir()
	fs := file.New(store.Config{DataDir: filepath.Join(dir, "data")})
	if err := fs.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	orig := time.Now().Add(-time.Minute)
	if err := fs.Devices().Create(context.Background(), &capture.Device{
		App: "com.example.integrating", Did: "d1", Name: "n", LastSeenAt: orig,
	}); err != nil {
		t.Fatal(err)
	}
	// Let the debounced save land.
	onDisk := waitForDataFile(t, filepath.Join(dir, "data"))
	if len(onDisk.Devices) != 1 || !onDisk.Devices[0].LastSeenAt.Equal(orig) {
		t.Fatalf("unexpected on-disk device after create: %+v", onDisk.Devices)
	}

	// The memory-only update must be visible in memory…
	newSeen := time.Now()
	if err := fs.Devices().UpdateLastSeen(context.Background(), "com.example.integrating", "d1", newSeen); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Devices().Get(context.Background(), "com.example.integrating", "d1")
	if err != nil || !got.LastSeenAt.Equal(newSeen) {
		t.Fatalf("in-memory LastSeenAt not updated: %+v err=%v", got, err)
	}
	// …but never flushed: the on-disk value stays the original one.
	raw, err := os.ReadFile(filepath.Join(dir, "data", "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var again persistedData
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if !again.Devices[0].LastSeenAt.Equal(orig) {
		t.Fatalf("UpdateLastSeen leaked to disk: got %v, want %v", again.Devices[0].LastSeenAt, orig)
	}
}

// TestUpdateRequestCountIsMemoryOnly mirrors the above for the per-batch
// session count bump.
func TestUpdateRequestCountIsMemoryOnly(t *testing.T) {
	dir := t.TempDir()
	fs := file.New(store.Config{DataDir: filepath.Join(dir, "data")})
	if err := fs.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	if err := fs.CaptureSessions().Create(context.Background(), &capture.CaptureSession{
		ID: "s1", App: "com.example.integrating", Did: "d1",
		Status: capture.SessionStatusCapturing, StartedAt: time.Now(), RequestCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	onDisk := waitForDataFile(t, filepath.Join(dir, "data"))
	if len(onDisk.CaptureSessions) != 1 || onDisk.CaptureSessions[0].RequestCount != 3 {
		t.Fatalf("unexpected on-disk session after create: %+v", onDisk.CaptureSessions)
	}

	if err := fs.CaptureSessions().UpdateRequestCount(context.Background(), "s1", 42); err != nil {
		t.Fatal(err)
	}
	got, err := fs.CaptureSessions().Get(context.Background(), "s1")
	if err != nil || got.RequestCount != 42 {
		t.Fatalf("in-memory RequestCount not updated: %+v err=%v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "data", "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var again persistedData
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if again.CaptureSessions[0].RequestCount != 3 {
		t.Fatalf("UpdateRequestCount leaked to disk: got %d, want 3", again.CaptureSessions[0].RequestCount)
	}
}
