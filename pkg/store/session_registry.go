package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: session_registry.go
// Capture session lifecycle: activate/end/query, viewer leases, background
// health check (pure move from capture_registry.go).
// ============================================================================
// Capture sessions
// ============================================================================

// ActivateSession activates a capture session for the device. A device has at
// most one capturing session: if one exists it is returned (created=false).
// Activating for an offline device is rejected with ErrDeviceOffline.
func (m *CaptureManager) ActivateSession(ctx context.Context, app, did string) (*capture.CaptureSession, bool, error) {
	if _, err := m.devices.Get(ctx, app, did); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, ErrDeviceNotRegistered
		}
		return nil, false, err
	}

	existing, err := m.activeSessionFor(ctx, app, did)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		sc := *existing
		return &sc, false, nil
	}

	// Reject activation for an offline device so the Web UI never shows a
	// "connected" state that can never start uploading.
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		return nil, false, err
	}
	if time.Since(d.LastSeenAt) > m.cfg.HeartbeatTimeout {
		return nil, false, ErrDeviceOffline
	}

	session := &capture.CaptureSession{
		ID:        id.ULID(),
		App:       app,
		Did:       did,
		Status:    capture.SessionStatusCapturing,
		StartedAt: time.Now(),
	}
	if err := m.sessions.Create(ctx, session); err != nil {
		return nil, false, err
	}
	return session, true, nil
}

// EndSession ends a capture session (forced disconnect, last viewer released,
// or heartbeat timeout). Idempotent: ending an already-ended session is a
// no-op. Viewer leases for the session are cleared.
//
// M2 (O3 48h 保留): ending a session marks it ended and stamps RetainUntil
// (now + TrafficRetention, default 48h). The session record and its traffic
// are KEPT and stay queryable via ListSessionTraffic during the window — the
// Web history view reads them; after the window the janitor purges both. This
// replaces the M9 "结束即删" model: the record is no longer deleted on end.
//
// M8.6 (断开后可分享): share creation still works after disconnect — traffic
// stays in the session (index retained=false) and the owner resolves via the
// session record, which now survives the end. The separate retained store is
// no longer written by EndSession; legacy retained data (pre-M2) is purged by
// PurgeExpiredRetainedTraffic as before.
//
// Mock rules are NOT deleted: they persist per device and are disabled on
// session end (M4/F4.5 决策13), re-enabled manually on the next session.
func (m *CaptureManager) EndSession(ctx context.Context, id string) error {
	// The whole end sequence runs under trafficMu, the same lock UploadTraffic
	// and ClearSessionTraffic hold for their authoritative session check +
	// traffic mutation. This serializes "end" with "upload/clear": an upload
	// either lands entirely before the end (its entries stay in the session's
	// traffic, queryable during the retention window) or is rejected after it.
	m.trafficMu.Lock()
	s, err := m.sessions.Get(ctx, id)
	if err != nil {
		m.trafficMu.Unlock()
		if errors.Is(err, ErrNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	if s.Status == capture.SessionStatusEnded {
		m.trafficMu.Unlock()
		return nil
	}

	m.viewerMu.Lock()
	delete(m.viewers, id)
	m.viewerMu.Unlock()

	// M2 (O3): mark ended + retention deadline instead of deleting the record
	// and moving traffic to the retained store. Traffic stays in m.traffic[id];
	// the ID index keeps retained=false, and ownership still resolves through
	// the (now persisted) session record.
	now := time.Now()
	sc := *s
	sc.Status = capture.SessionStatusEnded
	endAt := now
	sc.EndedAt = &endAt
	retainUntil := now.Add(m.cfg.TrafficRetention)
	sc.RetainUntil = &retainUntil
	if err := m.sessions.Update(ctx, &sc); err != nil {
		m.trafficMu.Unlock()
		return err
	}
	m.trafficMu.Unlock()

	// M4 (F4.5/决策13): any session end disables all of the device mock rules;
	// they stay but must be re-enabled manually.
	m.disableDeviceRules(ctx, s.App, s.Did)
	return nil
}

// ListSessions lists capture sessions (most recent first), delegated to the store.
func (m *CaptureManager) ListSessions(ctx context.Context, filter *SessionFilter) ([]*capture.CaptureSession, error) {
	return m.sessions.List(ctx, filter)
}

// GetSession returns a single capture session.
func (m *CaptureManager) GetSession(ctx context.Context, id string) (*capture.CaptureSession, error) {
	s, err := m.sessions.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	// O3 / v0.10.0: an ended session whose 48h retention window (RetainUntil)
	// has passed is gone for readers even if the janitor has not purged the
	// record yet — same expiry-first semantics as ListSessionTraffic.
	if s.Status == capture.SessionStatusEnded && s.RetainUntil != nil && time.Now().After(*s.RetainUntil) {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// ============================================================================
// Viewer leases
// ============================================================================

// RegisterViewer registers or renews a page-level viewer lease on a session.
// Registration is only allowed while the session is capturing; renewing an
// existing viewer refreshes the TTL without touching the store. The session
// ends when its viewer count drops to zero (last page closed).
func (m *CaptureManager) RegisterViewer(ctx context.Context, sessionID, viewerID, label string) (*capture.ViewerLease, error) {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if s.Status == capture.SessionStatusEnded {
		return nil, ErrSessionEnded
	}

	now := time.Now()
	lease := capture.ViewerLease{
		ViewerID:   viewerID,
		Label:      label,
		TTLSeconds: int(m.cfg.ViewerTTL.Seconds()),
		ExpiresAt:  now.Add(m.cfg.ViewerTTL),
	}

	m.viewerMu.Lock()
	viewers, ok := m.viewers[sessionID]
	if !ok {
		viewers = make(map[string]capture.ViewerLease)
		m.viewers[sessionID] = viewers
	}
	_, exists := viewers[viewerID]
	viewers[viewerID] = lease
	count := len(viewers)
	m.viewerMu.Unlock()

	if !exists {
		// First registration of this viewer: persist the new viewer count.
		sc := *s
		sc.ViewerCount = count
		if err := m.sessions.Update(ctx, &sc); err != nil {
			return nil, err
		}
	}
	return &lease, nil
}

// ReleaseViewer removes a viewer lease (page closed). If the last viewer is
// released while the session is capturing, the session ends. Idempotent:
// releasing an unknown viewer succeeds without error.
func (m *CaptureManager) ReleaseViewer(ctx context.Context, sessionID, viewerID string) error {
	m.viewerMu.Lock()
	viewers, ok := m.viewers[sessionID]
	var count int
	if ok {
		delete(viewers, viewerID)
		count = len(viewers)
		if count == 0 {
			delete(m.viewers, sessionID)
		}
	}
	m.viewerMu.Unlock()
	if !ok {
		// No leases for this session at all — nothing to do.
		return nil
	}

	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // session already deleted; lease state is gone with it
		}
		return err
	}

	// M9: last viewer released while capturing => the session ends and its
	// record is deleted (EndSession: clears traffic, disables rules, removes).
	if count == 0 && s.Status == capture.SessionStatusCapturing {
		return m.EndSession(ctx, sessionID)
	}

	sc := *s
	sc.ViewerCount = count
	if err := m.sessions.Update(ctx, &sc); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// Background health check
// ============================================================================

// StartHealthCheck starts a background goroutine that:
//  1. ends capture sessions whose device heartbeat has exceeded the timeout
//     (device offline => session ended, 僵尸清理兜底), and
//  2. garbage-collects expired viewer leases, ending a session when its last
//     lease expires without a page-close event (beforeunload is unreliable).
//     A single hourly janitor also purges expired mock rules, expired ended
//     sessions (O3 48h retention), retained traffic, QR pairing tokens (M9) and
//     share snapshots (M8.5) — one ticker instead of five (4.22: the janitors
//     are independent and cheap, and running them sequentially in the same
//     goroutine loses nothing).
func (m *CaptureManager) StartHealthCheck(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()

		ticker := time.NewTicker(m.cfg.HeartbeatTimeout / 2)
		defer ticker.Stop()
		hourly := time.NewTicker(time.Hour)
		defer hourly.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.checkDeviceHealth(ctx)
				m.checkViewerLeases(ctx)
			case <-hourly.C:
				m.LogDataFileSize() // O2.4 存储水位：每小时一行 data.json 体积
				m.PurgeExpiredRules(ctx)
				m.PurgeExpiredEndedSessions(ctx)
				m.PurgeExpiredRetainedTraffic(ctx)
				m.PurgeExpiredPairingTokens(ctx)
				m.PurgeExpiredShares(ctx)
			}
		}
	}()
}

// PurgeExpiredEndedSessions deletes ended sessions whose retention window
// (RetainUntil) has passed, together with their in-memory traffic (O3 48h 保留:
// 会话结束后流量保留 48h，之后 janitor 清理释放内存). It coexists with the 7-day
// mock-rule sliding cleanup (PurgeExpiredRules) — the two janitors are
// independent. Best-effort; called hourly by the health check. A session whose
// record is already gone is skipped; traffic keys without an owning session are
// swept by purgeRetainedBefore's orphan pass.
func (m *CaptureManager) PurgeExpiredEndedSessions(ctx context.Context) {
	now := time.Now()
	all, err := m.sessions.List(ctx, nil)
	if err != nil {
		m.log.Warn("capture health check: list sessions for purge failed", "error", err)
		return
	}
	for _, s := range all {
		if s.Status != capture.SessionStatusEnded || s.RetainUntil == nil {
			continue
		}
		if !now.After(*s.RetainUntil) {
			continue
		}
		// Re-check under trafficMu so a concurrent EndSession/UploadTraffic
		// cannot interleave between the list snapshot and the delete.
		m.trafficMu.Lock()
		cur, err := m.sessions.Get(ctx, s.ID)
		if err != nil {
			m.trafficMu.Unlock()
			continue // already gone; nothing to purge
		}
		if cur.Status == capture.SessionStatusEnded && cur.RetainUntil != nil && now.After(*cur.RetainUntil) {
			m.unindexTraffic(m.traffic[s.ID])
			delete(m.traffic, s.ID)
			if err := m.sessions.Delete(ctx, s.ID); err != nil {
				m.log.Warn("capture health check: purge expired ended session failed", "session", s.ID, "error", err)
			}
		}
		m.trafficMu.Unlock()
	}
}

// Stop stops the background health-check goroutine. Safe to call multiple times.
func (m *CaptureManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.wg.Wait()
}

// checkDeviceHealth ends capture sessions of devices whose heartbeat timed out.
func (m *CaptureManager) checkDeviceHealth(ctx context.Context) {
	devices, err := m.devices.List(ctx, nil)
	if err != nil {
		m.log.Warn("capture health check: list devices failed", "error", err)
		return
	}

	now := time.Now()
	for _, d := range devices {
		if now.Sub(d.LastSeenAt) <= m.cfg.HeartbeatTimeout {
			continue
		}
		session, err := m.activeSessionFor(ctx, d.App, d.Did)
		if err != nil {
			m.log.Warn("capture health check: find session failed", "app", d.App, "did", d.Did, "error", err)
			continue
		}
		if session != nil && session.Status == capture.SessionStatusCapturing {
			m.log.Info("device heartbeat timeout, ending capture session",
				"app", d.App, "did", d.Did, "session", session.ID)
			if err := m.EndSession(ctx, session.ID); err != nil {
				m.log.Warn("capture health check: end session failed", "session", session.ID, "error", err)
			}
		}
	}
}

// checkViewerLeases garbage-collects expired viewer leases.
func (m *CaptureManager) checkViewerLeases(ctx context.Context) {
	now := time.Now()

	m.viewerMu.Lock()
	type expired struct {
		sessionID string
		viewerID  string
	}
	var toDelete []expired
	for sessionID, viewers := range m.viewers {
		for viewerID, lease := range viewers {
			if now.After(lease.ExpiresAt) {
				toDelete = append(toDelete, expired{sessionID: sessionID, viewerID: viewerID})
			}
		}
	}
	// Sessions whose last viewer expired while capturing.
	var toEnd []string
	for _, e := range toDelete {
		viewers := m.viewers[e.sessionID]
		delete(viewers, e.viewerID)
		if len(viewers) == 0 {
			delete(m.viewers, e.sessionID)
			toEnd = append(toEnd, e.sessionID)
		}
	}
	m.viewerMu.Unlock()

	for _, sessionID := range toEnd {
		s, err := m.sessions.Get(ctx, sessionID)
		if err != nil {
			continue
		}
		if s.Status != capture.SessionStatusCapturing {
			continue
		}
		// M9: last viewer lease expired => end = delete the session record.
		if err := m.EndSession(ctx, sessionID); err != nil {
			m.log.Warn("capture health check: end session after viewer expiry failed", "session", sessionID, "error", err)
		}
	}
}

// activeSessionFor returns the capturing session of a device, or nil.
func (m *CaptureManager) activeSessionFor(ctx context.Context, app, did string) (*capture.CaptureSession, error) {
	active, err := m.sessions.List(ctx, &SessionFilter{
		App:    &app,
		Did:    &did,
		Status: &activeStatus,
	})
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	return active[0], nil
}

// activeStatus is a shared pointer to the capturing session status used in filters.
var activeStatus = capture.SessionStatusCapturing

// ============================================================================
// O2.4 存储水位监控
// ============================================================================

// DefaultDataFileWarnBytes is the data.json size watermark at which the
// storage-size check logs at WARN level (500MB, non-blocking).
const DefaultDataFileWarnBytes = 500 * 1024 * 1024

// SetDataFilePath points the storage-watermark check at the persisted
// data.json file (O2.4). An empty path disables the check (tests / pure
// in-memory runs).
func (m *CaptureManager) SetDataFilePath(p string) {
	m.dataFile = p
}

// LogDataFileSize emits one line with the current data.json size (O2.4):
// INFO when under the watermark, WARN at or above it. Best-effort and
// non-blocking; a missing file is reported as size 0 without error spam.
func (m *CaptureManager) LogDataFileSize() {
	if m.dataFile == "" {
		return
	}
	fi, err := os.Stat(m.dataFile)
	if err != nil {
		m.log.Info("data.json size", "path", m.dataFile, "bytes", 0, "exists", false)
		return
	}
	n := fi.Size()
	if n >= DefaultDataFileWarnBytes {
		m.log.Warn("data.json size exceeds warning threshold (O2.4)",
			"path", m.dataFile, "bytes", n, "human", humanBytes(n),
			"warnThreshold", DefaultDataFileWarnBytes)
		return
	}
	m.log.Info("data.json size (O2.4)",
		"path", m.dataFile, "bytes", n, "human", humanBytes(n))
}

// humanBytes renders a byte count in a compact human-readable form.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
