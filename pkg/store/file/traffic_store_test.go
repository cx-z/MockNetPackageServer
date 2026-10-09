package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// openFileStore opens a FileStore at dir with default sub-directories. Used by
// restart-simulation tests where the caller owns Close/reopen explicitly.
func openFileStore(t *testing.T, dir string) *FileStore {
	t.Helper()
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

func TestTrafficStore_RoundtripAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	fs1 := openFileStore(t, dir)
	ts := fs1.Traffic()
	entries := []*capture.TrafficEntry{
		{ID: "id-1", Seq: 1, Method: "GET", URL: "http://a.com/1", Timestamp: time.Now()},
		{ID: "id-2", Seq: 2, Method: "POST", URL: "http://a.com/2", Timestamp: time.Now()},
	}
	if err := ts.SaveSessionTraffic(ctx, "sess-1", entries); err != nil {
		t.Fatalf("SaveSessionTraffic() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "traffic", "sess-1.json")); err != nil {
		t.Fatalf("archive file not created: %v", err)
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// Restart: a fresh FileStore over the same dir must load the archive.
	fs2 := openFileStore(t, dir)
	defer fs2.Close()
	got, err := fs2.Traffic().LoadAllSessionTraffic(ctx)
	if err != nil {
		t.Fatalf("LoadAllSessionTraffic() = %v", err)
	}
	list := got["sess-1"]
	if len(list) != 2 || list[0].URL != "http://a.com/1" || list[1].Seq != 2 || list[1].Method != "POST" {
		t.Errorf("restored = %+v, want original 2 entries with fields intact", list)
	}
}

func TestTrafficStore_EmptySaveRemovesFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fs := openFileStore(t, dir)
	defer fs.Close()
	ts := fs.Traffic()

	entries := []*capture.TrafficEntry{{ID: "x", Seq: 1, Method: "GET", URL: "http://a.com", Timestamp: time.Now()}}
	if err := ts.SaveSessionTraffic(ctx, "s", entries); err != nil {
		t.Fatalf("SaveSessionTraffic() = %v", err)
	}
	if err := ts.SaveSessionTraffic(ctx, "s", nil); err != nil {
		t.Fatalf("SaveSessionTraffic(empty) = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "traffic", "s.json")); !os.IsNotExist(err) {
		t.Errorf("archive file not removed after empty save: %v", err)
	}
	all, err := ts.LoadAllSessionTraffic(ctx)
	if err != nil || len(all) != 0 {
		t.Errorf("LoadAllSessionTraffic() = %v, %v; want empty map", all, err)
	}
}

func TestTrafficStore_DeleteIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fs := openFileStore(t, dir)
	defer fs.Close()
	ts := fs.Traffic()
	if err := ts.DeleteSessionTraffic(ctx, "never-existed"); err != nil {
		t.Errorf("DeleteSessionTraffic(missing) = %v, want nil (idempotent)", err)
	}
}

func TestTrafficStore_LoadSkipsTmpAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fs := openFileStore(t, dir)
	defer fs.Close()
	ts := fs.Traffic()

	good := []*capture.TrafficEntry{{ID: "a", Seq: 1, Method: "GET", URL: "http://a.com", Timestamp: time.Now()}}
	if err := ts.SaveSessionTraffic(ctx, "good", good); err != nil {
		t.Fatalf("SaveSessionTraffic() = %v", err)
	}
	// A stale tmp from a crashed atomic write and a corrupt file must be
	// skipped without failing the whole load.
	_ = os.WriteFile(filepath.Join(dir, "traffic", "good.json.tmp"), []byte("partial"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "traffic", "bad.json"), []byte("{not json"), 0600)

	all, err := ts.LoadAllSessionTraffic(ctx)
	if err != nil {
		t.Fatalf("LoadAllSessionTraffic() = %v", err)
	}
	if len(all) != 1 || len(all["good"]) != 1 {
		t.Errorf("LoadAllSessionTraffic() = %v, want only 'good' with 1 entry", all)
	}
}
