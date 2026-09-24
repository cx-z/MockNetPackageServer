package store

import (
	"context"
	"errors"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: traffic_registry.go
// Session-scoped traffic: upload, query, delete/clear, retained store for
// ended sessions (pure move from capture_registry.go).
// ============================================================================
// Traffic (session-scoped, runtime-only)
// ============================================================================

// retainedSession holds the traffic of an ended capture session for a bounded
// window (RetainedTrafficTTL) so records the user saw on the page can still be
// shared after disconnect (M8.6). M9 list semantics are preserved: ended
// sessions are deleted and never listed again; retained entries are reachable
// only by ID. App/Did is kept because the session record is gone, and share
// creation must still verify device ownership.
type retainedSession struct {
	App     string
	Did     string
	EndedAt time.Time
	Entries []*capture.TrafficEntry
}

// UploadTraffic appends a batch of traffic entries to a capturing session
// (全量抓包, contract POST /traffic). Only traffic for an active session is
// accepted: an unknown session returns ErrSessionNotFound, an ended session
// returns ErrSessionEnded. Isolation (requirement 决策 #6) is enforced — the
// (app, did) carried by the upload must match the session's owning device.
//
// Partially-accepted semantics: entries with an empty method, empty url, or
// zero timestamp are dropped (contract required fields); the returned count is
// the number of entries actually stored. Record IDs and SessionID are
// server-generated here. The session's RequestCount is incremented and
// persisted, so it keeps its final value after the session ends.
func (m *CaptureManager) UploadTraffic(ctx context.Context, app, did, sessionID string, entries []*capture.TrafficEntry) (int, error) {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrSessionNotFound
		}
		return 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		return 0, ErrSessionEnded
	}
	if s.App != app || s.Did != did {
		// The upload does not belong to this device; report the session as
		// unknown to avoid leaking its existence (isolation).
		return 0, ErrSessionNotFound
	}

	stored := make([]*capture.TrafficEntry, 0, len(entries))
	for _, e := range entries {
		if e == nil || e.Method == "" || e.URL == "" || e.Timestamp.IsZero() {
			continue
		}
		c := *e
		c.ID = id.ULID()
		c.SessionID = sessionID
		stored = append(stored, &c)
	}
	if len(stored) == 0 {
		return 0, nil
	}

	m.trafficMu.Lock()
	m.traffic[sessionID] = append(m.traffic[sessionID], stored...)
	m.trafficMu.Unlock()

	sc := *s
	sc.RequestCount += len(stored)
	if err := m.sessions.Update(ctx, &sc); err != nil {
		return 0, err
	}

	// M4: a mocked upload is proof the rule was hit; refresh its LastUsedAt so
	// the sliding cleanup window starts over. Heartbeats/polling don't reach here.
	if hits := collectHits(stored); len(hits) > 0 {
		m.touchHitRules(ctx, app, did, hits)
	}
	return len(stored), nil
}

// collectHits returns the set of (method, path) interfaces that were actually
// mocked in this batch (deduplicated).
func collectHits(entries []*capture.TrafficEntry) map[interfaceKey]bool {
	hits := make(map[interfaceKey]bool)
	for _, e := range entries {
		if e.Mocked {
			hits[interfaceKey{method: e.Method, path: e.Path}] = true
		}
	}
	return hits
}

// touchHitRules refreshes LastUsedAt of every enabled rule matching a hit
// interface. It is best-effort: store errors are logged, never propagated to
// the traffic upload path.
func (m *CaptureManager) touchHitRules(ctx context.Context, app, did string, hits map[interfaceKey]bool) {
	all, err := m.rules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		m.log.Warn("rule janitor: list rules for hit-touch failed", "error", err)
		return
	}
	now := time.Now()
	for _, r := range all {
		if !r.Enabled {
			continue
		}
		if !hits[interfaceKey{method: r.Method, path: r.Path}] {
			continue
		}
		r.LastUsedAt = now
		if err := m.rules.Update(ctx, r); err != nil {
			m.log.Warn("rule janitor: touch hit rule failed", "rule", r.ID, "error", err)
		}
	}
}

// ListSessionTraffic returns a session's traffic entries in arrival order
// (request timeline, ascending), with limit/offset paging and the total count.
// For an ended session the temporary traffic has been cleared, so an empty
// list with total 0 is returned (contract: ended session => 200 + entries=[]
// + total=0); an unknown session returns ErrSessionNotFound.
func (m *CaptureManager) ListSessionTraffic(ctx context.Context, sessionID string, limit, offset int) ([]*capture.TrafficEntry, int, error) {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, 0, ErrSessionNotFound
		}
		return nil, 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		return []*capture.TrafficEntry{}, 0, nil
	}

	m.trafficMu.RLock()
	entries := m.traffic[sessionID]
	m.trafficMu.RUnlock()

	total := len(entries)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]*capture.TrafficEntry, 0, end-offset)
	for _, e := range entries[offset:end] {
		c := *e
		out = append(out, &c)
	}
	return out, total, nil
}

// GetTraffic returns a single traffic entry by its server-generated ID. The
// entry may live in an active session or in the retained store of an ended
// session (M8.6 断开后可分享, bounded by RetainedTrafficTTL). Entries that
// never existed or whose retention window has passed are reported as not
// found (contract: 404 not_found).
func (m *CaptureManager) GetTraffic(ctx context.Context, id string) (*capture.TrafficEntry, error) {
	m.trafficMu.RLock()
	for _, entries := range m.traffic {
		for _, e := range entries {
			if e.ID == id {
				c := *e
				m.trafficMu.RUnlock()
				return &c, nil
			}
		}
	}
	m.trafficMu.RUnlock()

	m.retainedMu.RLock()
	defer m.retainedMu.RUnlock()
	for _, rs := range m.retained {
		for _, e := range rs.Entries {
			if e.ID == id {
				c := *e
				return &c, nil
			}
		}
	}
	return nil, ErrNotFound
}

// GetTrafficWithOwner returns the owning device (app, did) of a traffic entry,
// whether the entry lives in an active session or in the retained store of an
// ended session. It is used to authorize share creation after the owning
// session record is gone (M9 deletes it on end). Returns ErrNotFound when the
// entry does not exist.
func (m *CaptureManager) GetTrafficWithOwner(ctx context.Context, id string) (app, did string, err error) {
	m.trafficMu.RLock()
	for sid, entries := range m.traffic {
		for _, e := range entries {
			if e.ID == id {
				m.trafficMu.RUnlock()
				s, err := m.sessions.Get(ctx, sid)
				if err != nil {
					return "", "", ErrNotFound
				}
				return s.App, s.Did, nil
			}
		}
	}
	m.trafficMu.RUnlock()

	m.retainedMu.RLock()
	defer m.retainedMu.RUnlock()
	for _, rs := range m.retained {
		for _, e := range rs.Entries {
			if e.ID == id {
				return rs.App, rs.Did, nil
			}
		}
	}
	return "", "", ErrNotFound
}

// DeleteTraffic deletes a single traffic entry by its server-generated ID
// (M9.5: Web per-row "删除"). Traffic is session-scoped temporary data; the
// owning session's persisted RequestCount is decremented when the session still
// exists. An unknown ID (including traffic of an already-deleted session)
// reports ErrNotFound — the caller treats delete as best-effort idempotent.
func (m *CaptureManager) DeleteTraffic(ctx context.Context, id string) error {
	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	for sid, entries := range m.traffic {
		for i, e := range entries {
			if e.ID != id {
				continue
			}
			m.traffic[sid] = append(entries[:i], entries[i+1:]...)
			// Keep the persisted session count in sync (best-effort; sessions
			// may already be gone under M9 delete-on-end semantics).
			if s, err := m.sessions.Get(ctx, sid); err == nil && s.RequestCount > 0 {
				s.RequestCount--
				_ = m.sessions.Update(ctx, s)
			}
			return nil
		}
	}
	return ErrNotFound
}

// ClearSessionTraffic clears all traffic of an active session and resets its
// request count to 0 (M9.5: Web "清空日志" — the timeline restarts, new
// uploads accumulate again). An unknown session returns ErrSessionNotFound;
// an ended session is rejected (M9: ended sessions are deleted anyway).
func (m *CaptureManager) ClearSessionTraffic(ctx context.Context, sessionID string) error {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	if s.Status == capture.SessionStatusEnded {
		return ErrSessionEnded
	}
	m.trafficMu.Lock()
	delete(m.traffic, sessionID)
	m.trafficMu.Unlock()

	sc := *s
	sc.RequestCount = 0
	return m.sessions.Update(ctx, &sc)
}

// ============================================================================
// Retained traffic janitor (M8.6 断开后可分享)
// ============================================================================

// PurgeExpiredRetainedTraffic drops retained traffic of ended sessions older
// than RetainedTrafficTTL. Retained traffic exists only to keep page records
// shareable after disconnect, so it is bounded by the same window as the
// share-link TTL. Best-effort; called hourly by the health check.
func (m *CaptureManager) PurgeExpiredRetainedTraffic(ctx context.Context) {
	m.purgeRetainedBefore(ctx, time.Now().Add(-m.cfg.RetainedTrafficTTL))
}

// purgeRetainedBefore removes every retained session ended before cutoff.
func (m *CaptureManager) purgeRetainedBefore(ctx context.Context, cutoff time.Time) {
	m.retainedMu.Lock()
	defer m.retainedMu.Unlock()
	for sid, rs := range m.retained {
		if rs.EndedAt.Before(cutoff) {
			delete(m.retained, sid)
		}
	}
}
