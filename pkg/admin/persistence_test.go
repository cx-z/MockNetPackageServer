package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/getmockd/mockd/pkg/store/file"
)

// freePort reserves an ephemeral port and returns it (released for reuse).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestStartFailsWhenDataStoreOpenFails (4.14): persistence is the default
// contract. A data store that fails to open (corrupt data.json) means the
// save loop never runs, so every write would silently vanish on restart —
// startup must fail instead of pretending to be healthy.
func TestStartFailsWhenDataStoreOpenFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(0, WithDataDir(dir), WithAPIKeyDisabled())
	if err := api.Start(); err == nil {
		_ = api.Stop()
		t.Fatal("Start() with a failed data store = nil, want error")
	} else if !strings.Contains(err.Error(), "data store") {
		t.Errorf("Start() error = %q, want it to name the data store", err)
	}
}

// TestStartWithNoPersistSurvivesStoreFailure (4.14): --no-persist is the
// explicit escape hatch — it degrades to an in-memory-only run instead of
// failing startup.
func TestStartWithNoPersistSurvivesStoreFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(0, WithDataDir(dir), WithAPIKeyDisabled(), WithNoPersist())
	if err := api.Start(); err != nil {
		t.Fatalf("Start() with --no-persist = %v, want nil", err)
	}
	_ = api.Stop()
}

// TestStopDrainsInFlightRequestsBeforeClosingStore (4.15): shutdown must drain
// the HTTP server BEFORE closing the data store, so an in-flight handler's
// write still lands in the final save. With the pre-fix order (store closed
// first, then Shutdown), the drained handler wrote into an already-closed
// store and the device silently vanished from disk.
func TestStopDrainsInFlightRequestsBeforeClosingStore(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)

	api := NewAPI(port, WithDataDir(dir), WithAPIKeyDisabled())
	// Wrap the real mux with a slow-path hook: /slow registers a device while
	// the request is in flight and only returns once released.
	original := api.httpServer.Handler
	released := make(chan struct{})
	api.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			if _, err := api.captureManager.RegisterDevice(r.Context(),
				&capture.Device{App: "app", Did: "during-stop"}); err != nil {
				t.Errorf("register during stop: %v", err)
			}
			<-released
			w.WriteHeader(http.StatusNoContent)
			return
		}
		original.ServeHTTP(w, r)
	})
	if err := api.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	reqDone := make(chan error, 1)
	go func() {
		_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/slow", port))
		reqDone <- err
	}()

	// Let the request reach the handler, then start shutdown.
	time.Sleep(300 * time.Millisecond)
	stopDone := make(chan error, 1)
	go func() { stopDone <- api.Stop() }()

	// Stop must now be blocked draining the in-flight request; release it.
	select {
	case <-reqDone:
		t.Fatal("request completed before Stop returned — test setup broken")
	case <-time.After(200 * time.Millisecond):
	}
	close(released)

	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if err := <-reqDone; err != nil {
		t.Fatalf("GET /slow = %v", err)
	}

	// The drained handler's write must be persisted: the store was closed only
	// AFTER the in-flight request finished (4.15 order).
	fs := file.New(store.Config{DataDir: dir})
	if err := fs.Open(context.Background()); err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	devs, err := fs.Devices().List(context.Background(), nil)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	found := false
	for _, d := range devs {
		if d.Did == "during-stop" {
			found = true
		}
	}
	if !found {
		t.Fatal("in-flight write lost: device not persisted after Stop()")
	}
}
