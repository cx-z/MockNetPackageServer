package store

import (
	"context"
	"errors"
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
// M9 (会话结束即删): ending a session deletes its record — the user's view
// keeps only the current session, no history. Mock rules are NOT deleted:
// they persist per device and are disabled on session end (M4/F4.5 决策13),
// re-enabled manually on the next session.
//
// M8.6 (断开后可分享): the session's traffic is NOT discarded — it moves to
// the retained store so records already shown on the page stay resolvable by
// ID (share creation) for RetainedTrafficTTL. The list contract is unchanged:
// an ended session is deleted and never listed again.
func (m *CaptureManager) EndSession(ctx context.Context, id string) error {
	s, err := m.sessions.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	if s.Status == capture.SessionStatusEnded {
		return nil
	}

	m.viewerMu.Lock()
	delete(m.viewers, id)
	m.viewerMu.Unlock()

	// M8.6: move the session's traffic to the retained store instead of
	// deleting it, keeping the entries resolvable by ID for share creation
	// after disconnect (bounded by RetainedTrafficTTL, purged by the health
	// check). The owning device is recorded because the session record is
	// about to be deleted and ownership must stay verifiable.
	m.trafficMu.Lock()
	entries := m.traffic[id]
	delete(m.traffic, id)
	m.trafficMu.Unlock()
	if len(entries) > 0 {
		m.retainedMu.Lock()
		m.retained[id] = &retainedSession{
			App:     s.App,
			Did:     s.Did,
			EndedAt: time.Now(),
			Entries: entries,
		}
		m.retainedMu.Unlock()
	}

	// M9: 结束即删 — delete the session record instead of keeping an
	// "ended" entry. The device and rule stores are untouched.
	if err := m.sessions.Delete(ctx, id); err != nil {
		return err
	}
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
func (m *CaptureManager) StartHealthCheck(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()

		ticker := time.NewTicker(m.cfg.HeartbeatTimeout / 2)
		defer ticker.Stop()
		ruleTicker := time.NewTicker(time.Hour)
		defer ruleTicker.Stop()
		retainedTicker := time.NewTicker(time.Hour)
		defer retainedTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.checkDeviceHealth(ctx)
				m.checkViewerLeases(ctx)
			case <-ruleTicker.C:
				m.PurgeExpiredRules(ctx)
			case <-retainedTicker.C:
				m.PurgeExpiredRetainedTraffic(ctx)
			}
		}
	}()
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
