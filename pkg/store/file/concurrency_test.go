package file_test

// Concurrency regression tests for the CaptureManager + file store stack.
// These exercise the exact interleavings that were reported as data races
// (Heartbeat mutating the shared Device pointer outside the store lock while
// ListDevices reads it). They must be run with -race (CI gate).

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/getmockd/mockd/pkg/store/file"
)

// newTestManager opens a fresh FileStore in a temp dir and returns a
// CaptureManager with one device registered.
func newTestManager(t *testing.T) (*store.CaptureManager, *file.FileStore) {
	t.Helper()
	dir := t.TempDir()
	fs := file.New(store.Config{DataDir: filepath.Join(dir, "data")})
	if err := fs.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	ctx := context.Background()
	if _, err := m.CreateManualDevice(ctx, &capture.Device{
		App: "com.example.integrating", Did: "race-did", Name: "race", Owner: "u",
	}); err != nil {
		t.Fatal(err)
	}
	return m, fs
}

// runConcurrent hammers fnA and fnB in parallel for ~1.5s, then stops both.
func runConcurrent(t *testing.T, name string, fnA, fnB func() error) {
	t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	run := func(fn func() error) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := fn(); err != nil {
					errCh <- err
					return
				}
			}
		}
	}

	wg.Add(2)
	go run(fnA)
	go run(fnB)

	select {
	case err := <-errCh:
		t.Fatalf("%s: unexpected error: %v", name, err)
	case <-time.After(1500 * time.Millisecond):
	}
	close(stop)
	wg.Wait()
}

// TestEndSessionPreservesUploadedTrafficForRetention locks the P0-3 + O3
// semantics: entries uploaded before EndSession stay in the session (still
// resolvable by ID for share creation, and listable during the 48h window),
// and an upload after the end must be rejected with ErrSessionEnded.
func TestEndSessionPreservesUploadedTrafficForRetention(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	sess, _, err := m.ActivateSession(ctx, "com.example.integrating", "race-did")
	if err != nil {
		t.Fatal(err)
	}
	entries := []*capture.TrafficEntry{
		{Timestamp: time.Now(), Method: "GET", URL: "http://h/a", Path: "/a"},
		{Timestamp: time.Now(), Method: "POST", URL: "http://h/b", Path: "/b"},
		{Timestamp: time.Now(), Method: "PUT", URL: "http://h/c", Path: "/c"},
	}
	n, err := m.UploadTraffic(ctx, "com.example.integrating", "race-did", sess.ID, entries)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("uploaded %d, want 3", n)
	}

	listed, total, err := m.ListSessionTraffic(ctx, sess.ID, 0, 0, store.TrafficFilter{})
	if err != nil || total != 3 || len(listed) != 3 {
		t.Fatalf("list before end: total=%d len=%d err=%v", total, len(listed), err)
	}
	ids := make([]string, len(listed))
	for i, e := range listed {
		ids[i] = e.ID
	}

	if err := m.EndSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	// O3: each entry stays resolvable by ID (session record kept).
	for _, id := range ids {
		if _, err := m.GetTraffic(ctx, id); err != nil {
			t.Fatalf("entry %s lost after end: %v", id, err)
		}
	}
	// The ended session stays listable during the retention window.
	if listed, total, err := m.ListSessionTraffic(ctx, sess.ID, 0, 0, store.TrafficFilter{}); err != nil || total != 3 || len(listed) != 3 {
		t.Fatalf("list after end: total=%d len=%d err=%v; want 3/3 (O3 retention)", total, len(listed), err)
	}
	// Upload after end is rejected (record kept => ErrSessionEnded).
	if _, err := m.UploadTraffic(ctx, "com.example.integrating", "race-did", sess.ID, entries[:1]); !errors.Is(err, store.ErrSessionEnded) {
		t.Fatalf("upload after end: got %v, want ErrSessionEnded", err)
	}
}

// TestConcurrentUploadAndEndSession exercises the P0-3 serialization under
// contention: concurrent uploaders must only ever see success or the two
// expected end-related errors — never a partially-committed failure or an
// orphaned in-memory key that a later purge cannot explain.
func TestConcurrentUploadAndEndSession(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	sess, _, err := m.ActivateSession(ctx, "com.example.integrating", "race-did")
	if err != nil {
		t.Fatal(err)
	}

	const uploaders = 4
	stop := make(chan struct{})
	errCh := make(chan error, uploaders*64)
	var wg sync.WaitGroup
	for i := 0; i < uploaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					batch := make([]*capture.TrafficEntry, 5)
					for j := range batch {
						batch[j] = &capture.TrafficEntry{
							Timestamp: time.Now(),
							Method:    "GET",
							URL:       "http://h/x",
							Path:      "/x",
						}
					}
					_, err := m.UploadTraffic(ctx, "com.example.integrating", "race-did", sess.ID, batch)
					if err != nil {
						select {
						case errCh <- err:
						default:
						}
					}
				}
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	if err := m.EndSession(ctx, sess.ID); err != nil {
		t.Fatalf("end session: %v", err)
	}
	close(stop)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if !errors.Is(err, store.ErrSessionNotFound) && !errors.Is(err, store.ErrSessionEnded) {
			t.Fatalf("uploader saw unexpected error: %v", err)
		}
	}

	// The end must fully win: the session is ended (record kept, O3) and its
	// traffic stays listable; the janitor must not panic on the traffic map.
	if _, _, err := m.ListSessionTraffic(ctx, sess.ID, 0, 0, store.TrafficFilter{}); err != nil {
		t.Fatalf("list after end: got %v, want data (O3 retention)", err)
	}
	m.PurgeExpiredEndedSessions(ctx)
}

// TestConcurrentRuleCreateSameInterfaceRaceFree (4.9): with the pre-fix code
// the conflict check and the create were separate store calls, so two
// concurrent creates on the same interface could both pass the check and
// yield the abnormal multi-enabled state. The atomic Mutate serializes
// check + insert + version bump: exactly one create wins, the rest get
// ErrRuleConflict, and no abnormal conflict is reported afterwards.
func TestConcurrentRuleCreateSameInterfaceRaceFree(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := m.CreateMockRule(ctx, "app", "d1", &capture.MockRuleInput{
				Method: "POST", Path: "/same", Enabled: true,
				Response: capture.MockResponse{StatusCode: 200},
				Source:   &capture.MockRuleSource{Method: "POST", Path: "/same"},
			}, nil)
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	conflicts := 0
	for err := range errCh {
		if !errors.Is(err, store.ErrRuleConflict) {
			t.Fatalf("unexpected error: %v", err)
		}
		conflicts++
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}
	views, conflictList, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 1 || len(conflictList) != 0 {
		t.Fatalf("want exactly 1 rule / 0 conflicts; got %d rules / %d conflicts / err=%v",
			len(views), len(conflictList), err)
	}
}

// TestRaceHeartbeatVsListDevices guards against the shared-pointer race:
// Heartbeat wrote d.LastSeenAt outside any lock while ListDevices read the
// same pointer under the store RLock.
func TestRaceHeartbeatVsListDevices(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	runConcurrent(t, "Heartbeat vs ListDevices",
		func() error {
			_, _, err := m.Heartbeat(ctx, "com.example.integrating", "race-did")
			return err
		},
		func() error {
			_, err := m.ListDevices(ctx, nil)
			return err
		})
}

// TestRaceHeartbeatVsGetDevice covers the same race through the single-device
// read path used by handlers and the health check.
func TestRaceHeartbeatVsGetDevice(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	runConcurrent(t, "Heartbeat vs GetDevice",
		func() error {
			_, _, err := m.Heartbeat(ctx, "com.example.integrating", "race-did")
			return err
		},
		func() error {
			_, err := m.GetDevice(ctx, "com.example.integrating", "race-did")
			return err
		})
}

// TestRaceRuleTouchVsList covers the same pattern on the rule store: the
// hourly janitor touches LastUsedAt on listed rules then Updates them, while
// concurrent rule listing reads the same pointers.
func TestRaceRuleTouchVsList(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	// One enabled rule so touchHitRules has something to update, and a
	// capturing session so UploadTraffic reaches the touch path.
	if _, _, err := m.CreateMockRule(ctx, "com.example.integrating", "race-did", &capture.MockRuleInput{
		Method: "GET", Path: "/p", Enabled: true, Response: capture.MockResponse{StatusCode: 200},
		Source: &capture.MockRuleSource{Method: "GET", Path: "/p"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sess, _, err := m.ActivateSession(ctx, "com.example.integrating", "race-did")
	if err != nil {
		t.Fatal(err)
	}

	runConcurrent(t, "touchHitRules vs ListActiveMockRules",
		func() error {
			// Force the LastUsedAt update path (mocked upload).
			_, err := m.UploadTraffic(ctx, "com.example.integrating", "race-did", sess.ID, []*capture.TrafficEntry{{
				Timestamp: time.Now(), Method: "GET", URL: "http://h/p", Path: "/p", Mocked: true,
			}})
			return err
		},
		func() error {
			_, _, _, err := m.ListActiveMockRules(ctx, "com.example.integrating", "race-did", 0)
			return err
		})
}
