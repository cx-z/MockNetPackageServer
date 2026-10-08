package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPairingTokenStore_CreateGetDeleteExpired(t *testing.T) {
	fs := newTestStore(t)
	s := fs.PairingTokens()
	ctx := context.Background()

	now := time.Now()
	valid := &account.PairingToken{Token: "tok-valid", User: "alice", App: "com.a", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	expired := &account.PairingToken{Token: "tok-expired", User: "bob", App: "com.a", CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)}
	if err := s.Create(ctx, valid); err != nil {
		t.Fatalf("Create(valid) = %v", err)
	}
	if err := s.Create(ctx, expired); err != nil {
		t.Fatalf("Create(expired) = %v", err)
	}

	// Duplicate token -> ErrAlreadyExists.
	if err := s.Create(ctx, &account.PairingToken{Token: "tok-valid", User: "alice", App: "com.a", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("Create(dup) = %v, want ErrAlreadyExists", err)
	}

	got, err := s.GetByToken(ctx, "tok-valid")
	if err != nil || got.User != "alice" || got.App != "com.a" {
		t.Fatalf("GetByToken = %+v, %v", got, err)
	}
	if _, err := s.GetByToken(ctx, "no-such"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetByToken(unknown) = %v, want ErrNotFound", err)
	}

	// DeleteExpired removes only the expired token.
	n, err := s.DeleteExpired(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v; want 1", n, err)
	}
	if _, err := s.GetByToken(ctx, "tok-expired"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired token still present: %v", err)
	}
	if _, err := s.GetByToken(ctx, "tok-valid"); err != nil {
		t.Errorf("valid token purged: %v", err)
	}
}

func TestCaptureManager_PairingTokenLifecycle(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// Issue + validate round-trip.
	tok, err := m.CreatePairingToken(ctx, "alice", "com.a")
	if err != nil {
		t.Fatalf("CreatePairingToken = %v", err)
	}
	if tok.Token == "" || tok.User != "alice" || tok.App != "com.a" || !tok.Valid(time.Now()) {
		t.Fatalf("token = %+v", tok)
	}
	if got, err := m.ValidatePairingToken(ctx, tok.Token, "com.a"); err != nil || got.User != "alice" {
		t.Fatalf("ValidatePairingToken = %+v, %v", got, err)
	}
	// Wrong app -> invalid.
	if _, err := m.ValidatePairingToken(ctx, tok.Token, "com.b"); !errors.Is(err, store.ErrPairingTokenInvalid) {
		t.Errorf("Validate(wrong app) = %v, want ErrPairingTokenInvalid", err)
	}
	// Garbage token -> invalid.
	if _, err := m.ValidatePairingToken(ctx, "garbage", "com.a"); !errors.Is(err, store.ErrPairingTokenInvalid) {
		t.Errorf("Validate(garbage) = %v, want ErrPairingTokenInvalid", err)
	}

	// RegisterDeviceWithPairing: unknown did -> created under the token user.
	d, err := m.RegisterDeviceWithPairing(ctx, captureDevice("com.a", "scan-1", ""), "alice")
	if err != nil {
		t.Fatalf("RegisterDeviceWithPairing = %v", err)
	}
	if d.Owner != "alice" || d.Name == "" || d.RegisteredAt.IsZero() {
		t.Errorf("created device = %+v", d)
	}

	// Same (app, did) with a different owner -> reused, owner unchanged .
	if _, err := m.RegisterDeviceWithPairing(ctx, captureDevice("com.a", "scan-1", "renamed"), "bob"); err != nil {
		t.Fatalf("RegisterDeviceWithPairing(reuse) = %v", err)
	}
	views, err := m.ListDevices(ctx, nil)
	if err != nil || len(views) != 1 {
		t.Fatalf("ListDevices = %d, %v; want 1", len(views), err)
	}
	if views[0].Owner != "alice" || views[0].Name == "renamed" {
		t.Errorf("reused device mutated: %+v (owner/name must be preserved)", views[0])
	}

	// PurgeExpiredPairingTokens is a no-op on fresh tokens but survives.
	m.PurgeExpiredPairingTokens(ctx)
	if _, err := m.ValidatePairingToken(ctx, tok.Token, "com.a"); err != nil {
		t.Errorf("token purged prematurely: %v", err)
	}
}

// captureDevice is a tiny constructor so the pairing tests read clearly.
func captureDevice(app, did, name string) *capture.Device {
	return &capture.Device{App: app, Did: did, Name: name, Platform: capture.PlatformIOS}
}

// TestRecordPairingUse : appends dids, dedupes repeats,
// and rejects unknown tokens.
func TestRecordPairingUse(t *testing.T) {
	ctx := context.Background()
	fs := newTestStore(t)
	tok := &account.PairingToken{
		Token:     "tok-record-use",
		User:      "u",
		App:       "com.example.integrating",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	require.NoError(t, fs.PairingTokens().Create(ctx, tok))

	now := time.Now()
	require.NoError(t, fs.PairingTokens().RecordPairingUse(ctx, tok.Token, "did-a", now))
	require.NoError(t, fs.PairingTokens().RecordPairingUse(ctx, tok.Token, "did-b", now.Add(time.Second)))
	// Dedup: same did again does not append.
	require.NoError(t, fs.PairingTokens().RecordPairingUse(ctx, tok.Token, "did-a", now.Add(2*time.Second)))

	got, err := fs.PairingTokens().GetByToken(ctx, tok.Token)
	require.NoError(t, err)
	require.Len(t, got.PairedDevices, 2)
	assert.Equal(t, "did-a", got.PairedDevices[0].Did)
	assert.Equal(t, "did-b", got.PairedDevices[1].Did)
	assert.Equal(t, now, got.PairedDevices[0].RegisteredAt)

	// Unknown token -> ErrNotFound.
	require.ErrorIs(t, fs.PairingTokens().RecordPairingUse(ctx, "nope", "did-x", now), store.ErrNotFound)
}
