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

// TestCaptureManager_UploadLongConn_Validation: unknown session →
// ErrSessionNotFound; ended session → ErrSessionEnded (same semantics as
// UploadTraffic, but longconn carries no app/did — session attribution comes
// from the path).
func TestCaptureManager_UploadLongConn_Validation(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	// Unknown session → ErrSessionNotFound.
	if _, err := m.UploadLongConn(ctx, "no-such-session", capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records:   []capture.LongConnectionRecordInput{longConnPacket("m1", time.Now())},
	}); !errors.Is(err, store.ErrSessionNotFound) {
		t.Errorf("UploadLongConn(unknown session) = %v, want ErrSessionNotFound", err)
	}

	// Ended session → ErrSessionEnded.
	if err := m.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("EndSession() = %v", err)
	}
	if _, err := m.UploadLongConn(ctx, s.ID, capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records:   []capture.LongConnectionRecordInput{longConnPacket("m1", time.Now())},
	}); !errors.Is(err, store.ErrSessionEnded) {
		t.Errorf("UploadLongConn(ended session) = %v, want ErrSessionEnded", err)
	}
}

// TestCaptureManager_UploadLongConn_AcceptAndCount: partially-accepted
// semantics + server-generated seq (independent from the traffic counter).
func TestCaptureManager_UploadLongConn_AcceptAndCount(t *testing.T) {
	m, fs := newCaptureManager(t, 0)
	m.SetLongConnStore(fs.LongConn())
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}

	now := time.Now()
	records := []capture.LongConnectionRecordInput{
		longConnPacket("m1", now), // kept: valid packet
		{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: now}, // dropped: packet without payload triplet
		{EventType: capture.LongConnectionEventConnect, TS: now},                      // dropped: empty channelId
		{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventSnapshot},      // dropped: snapshot without Snapshot, and zero TS
		longConnPacket("m2", now.Add(time.Millisecond)), // kept
	}
	count, err := m.UploadLongConn(ctx, s.ID, capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records:    records,
	})
	if err != nil {
		t.Fatalf("UploadLongConn() = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (2 of 5 kept)", count)
	}

	// Longconn does NOT touch RequestCount (独立额度, 不占用 HTTP 流量额度).
	got, err := m.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession() = %v", err)
	}
	if got.RequestCount != 0 {
		t.Errorf("RequestCount = %d, want 0 (longconn must not bump it)", got.RequestCount)
	}

	// Archive file persisted (best-effort) under <dataDir>/longconn/<sid>.json.
	archive := filepath.Join(fs.DataDir(), "longconn", s.ID+".json")
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("longconn archive file = %v, want it to exist at %s", err, archive)
	}
}

// longConnPacket builds a valid inbound packet record (carries payload).
func longConnPacket(msgID string, ts time.Time) capture.LongConnectionRecordInput {
	return capture.LongConnectionRecordInput{
		ChannelID:     "xcpush_tcp",
		EventType:     capture.LongConnectionEventPacket,
		TS:            ts,
		MsgID:         msgID,
		PayloadText:   `{"type":"chat"}`,
	}
}

// TestCaptureManager_RestoreLongConn: 跨重启恢复——upload → 新 manager
// （同一 store）RestoreLongConn → 记录恢复、seq 接续不跳号。
func TestCaptureManager_RestoreLongConn(t *testing.T) {
	m1, fs := newCaptureManager(t, 0)
	m1.SetLongConnStore(fs.LongConn())
	ctx := context.Background()

	if _, err := m1.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m1.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	now := time.Now()
	if _, err := m1.UploadLongConn(ctx, s.ID, capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records:   []capture.LongConnectionRecordInput{longConnPacket("m1", now), longConnPacket("m2", now.Add(time.Second))},
	}); err != nil {
		t.Fatalf("UploadLongConn() = %v", err)
	}

	// Simulate restart: a fresh manager over the same store.
	m2 := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), store.DefaultCaptureConfig())
	m2.SetLongConnStore(fs.LongConn())
	m2.RestoreLongConn(ctx)

	entries, total, err := m2.ListSessionLongConn(ctx, s.ID, capture.LongConnectionQuery{})
	if err != nil {
		t.Fatalf("ListSessionLongConn() = %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("restored total = %d (len %d), want 2", total, len(entries))
	}
	if entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Errorf("restored seqs = %d,%d, want 1,2", entries[0].Seq, entries[1].Seq)
	}

	// New upload after restore continues the seq counter (no reset to 1).
	if _, err := m2.UploadLongConn(ctx, s.ID, capture.LongConnUploadRequest{
		ChannelID: "xcpush_tcp",
		Records:   []capture.LongConnectionRecordInput{longConnPacket("m3", now.Add(2 * time.Second))},
	}); err != nil {
		t.Fatalf("UploadLongConn after restore = %v", err)
	}
	entries, _, _ = m2.ListSessionLongConn(ctx, s.ID, capture.LongConnectionQuery{})
	if got := entries[2].Seq; got != 3 {
		t.Errorf("seq after restore-continued upload = %d, want 3", got)
	}
}
