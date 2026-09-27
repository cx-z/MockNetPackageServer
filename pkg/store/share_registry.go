package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: share_registry.go
// Request share snapshots (M8.5): CreateShare/GetShare, TTL (pure move).
// ============================================================================
// M8.5: Request share snapshots
// ============================================================================

// ShareTTL is how long a share link stays valid (7 days, per product decision).
const ShareTTL = 7 * 24 * time.Hour

// ShareSnapshot is an independent, read-only copy of a single traffic entry
// published via a share link. It is decoupled from the owning session/traffic
// — clearing or deleting the session does not affect the share. The shareId
// is an unguessable UUID; no signature is needed because the link contains
// only the opaque ID and all data lives server-side.
type ShareSnapshot struct {
	ShareID   string                `json:"shareId"`
	CreatedAt time.Time             `json:"createdAt"`
	ExpiresAt time.Time             `json:"expiresAt"`
	Entry     *capture.TrafficEntry `json:"entry"`
}

// CreateShare copies the traffic entry identified by trafficID into a new
// independent share snapshot and returns it. The entry may come from an
// active session or from the retained store of an ended session (M8.6).
// Returns ErrNotFound if the entry does not exist (e.g. never existed, or its
// retention window has passed).
func (m *CaptureManager) CreateShare(ctx context.Context, trafficID string) (*ShareSnapshot, error) {
	entry, err := m.GetTraffic(ctx, trafficID)
	if err != nil {
		return nil, err
	}
	// Deep-copy the entry so later mutations to the original traffic do not
	// leak into the share. TrafficEntry contains maps, so a shallow copy is
	// not enough; JSON round-trip is simple and sufficient for a snapshot.
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("marshal share snapshot: %w", err)
	}
	var copy capture.TrafficEntry
	if err := json.Unmarshal(data, &copy); err != nil {
		return nil, fmt.Errorf("unmarshal share snapshot: %w", err)
	}
	copy.SessionID = "" // share must not expose internal session linkage

	now := time.Now()
	snap := &ShareSnapshot{
		ShareID:   id.UUID(),
		CreatedAt: now,
		ExpiresAt: now.Add(ShareTTL),
		Entry:     &copy,
	}

	// 4.8: persist the snapshot so the link survives server restarts for its
	// full 7-day validity. The snapshot is self-contained (a full copy of the
	// entry), so nothing else needs to survive.
	m.sharesMu.Lock()
	defer m.sharesMu.Unlock()
	if err := m.sharesStore.Create(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// GetShare returns a share snapshot by ID, or ErrNotFound if it does not
// exist or has expired. Expired shares are lazily purged on access (and
// periodically by the hourly janitor).
func (m *CaptureManager) GetShare(ctx context.Context, shareID string) (*ShareSnapshot, error) {
	m.sharesMu.RLock()
	all, err := m.sharesStore.List(ctx)
	m.sharesMu.RUnlock()
	if err != nil {
		return nil, err
	}
	for _, snap := range all {
		if snap.ShareID != shareID {
			continue
		}
		if time.Now().After(snap.ExpiresAt) {
			m.sharesMu.Lock()
			_ = m.sharesStore.Delete(ctx, shareID) // lazy purge (best-effort)
			m.sharesMu.Unlock()
			return nil, ErrNotFound
		}
		c := *snap
		return &c, nil
	}
	return nil, ErrNotFound
}

// PurgeExpiredShares deletes every share snapshot expired before now (hourly
// janitor; expired shares are also rejected lazily on GetShare, so this is
// housekeeping that keeps the persisted file bounded).
func (m *CaptureManager) PurgeExpiredShares(ctx context.Context) {
	if _, err := m.sharesStore.DeleteExpired(ctx, time.Now()); err != nil {
		m.log.Warn("capture health check: purge expired shares failed", "error", err)
	}
}
