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

// DefaultMaxSessionTrafficEntries is the default cap on the number of traffic
// entries kept per active session (4.10, O2.1). A long capture must not grow
// server memory without bound: once the cap is reached, new uploads replace
// the OLDEST entries (rolling window). The session's RequestCount still counts
// every accepted upload, so the Web badge ("共收到 N 个请求") keeps growing
// while the list shows the most recent MaxSessionTrafficEntries rows. The cap
// is a server-side contract bound: the SDK is never told — it just keeps
// uploading, and the newest data always wins. Overridable via
// --capture-session-max-entries (CaptureConfig.MaxSessionTrafficEntries).
const DefaultMaxSessionTrafficEntries = 20000

// DefaultTrafficRetention is how long an ended capture session and its traffic
// stay queryable before the janitor purges them (O3, from session end).
// Overridable via --capture-traffic-retention-hours
// (CaptureConfig.TrafficRetention).
const DefaultTrafficRetention = 48 * time.Hour

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

// trafficIndexEntry locates a traffic entry by its server-generated ID (4.22).
// retained=false ⇒ the entry lives in m.traffic[sid] and the owning device
// resolves via the session record; retained=true ⇒ the entry lives in
// m.retained[sid] (its session record is deleted under M9) and app/did snapshot
// the owning device at end time. The entry pointer is immutable once written
// (entries are never mutated after upload), so a lookup can safely copy its
// value while holding the index read lock.
type trafficIndexEntry struct {
	sid      string
	retained bool
	app      string
	did      string
	entry    *capture.TrafficEntry
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
//
// Bounded storage (4.10): each session keeps at most MaxSessionTrafficEntries
// entries; overflow drops the oldest (rolling window). RequestCount counts
// every accepted upload regardless of the window, so it may exceed the
// number of listable entries.
func (m *CaptureManager) UploadTraffic(ctx context.Context, app, did, sessionID string, entries []*capture.TrafficEntry) (int, error) {
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

	// Authoritative check inside trafficMu: EndSession holds the same lock
	// across its status check + traffic move + session delete, so an upload
	// either lands entirely before the end (its entries move to retained with
	// the session) or is rejected after it. Previously the check happened
	// outside the lock and a racing end could orphan the appended entries
	// (never retained/listed/purged) while the RequestCount update failed.
	m.trafficMu.Lock()
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		m.trafficMu.Unlock()
		if errors.Is(err, ErrNotFound) {
			return 0, ErrSessionNotFound
		}
		return 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		m.trafficMu.Unlock()
		return 0, ErrSessionEnded
	}
	if s.App != app || s.Did != did {
		// The upload does not belong to this device; report the session as
		// unknown to avoid leaking its existence (isolation).
		m.trafficMu.Unlock()
		return 0, ErrSessionNotFound
	}
	m.traffic[sessionID] = append(m.traffic[sessionID], stored...)
	// 4.10: bound per-session storage. Once the cap is hit, drop the oldest
	// entries (rolling window) so a long capture cannot grow memory without
	// limit. The fresh backing array also releases the old one for GC.
	// 4.22: the trimmed (oldest) entries leave the ID index together with the
	// slice, and only the surviving new entries enter it.
	existing := len(m.traffic[sessionID]) - len(stored)
	var trimmedOld int // how many pre-existing entries the window trim dropped
	if n := len(m.traffic[sessionID]); n > m.cfg.MaxSessionTrafficEntries {
		drop := n - m.cfg.MaxSessionTrafficEntries
		m.unindexTraffic(m.traffic[sessionID][:drop])
		trimmedOld = drop
		m.traffic[sessionID] = append([]*capture.TrafficEntry(nil), m.traffic[sessionID][drop:]...)
	}
	// The newest entries survive the trim; only an oversized batch (bigger than
	// the window itself) trims some of the just-appended entries.
	survivingNew := len(stored)
	if trimmedOld > existing {
		survivingNew -= trimmedOld - existing
	}
	if survivingNew > 0 {
		m.indexTraffic(sessionID, false, "", "", stored[len(stored)-survivingNew:])
	}
	// 4.4: the count bump rides every upload batch (~every 2s per capturing
	// device) — keep it memory-only (no dirty marking) so batches do not
	// rewrite the whole data file. After a restart the stale session is ended
	// by the heartbeat-timeout sweep before any user reads the count.
	if err := m.sessions.UpdateRequestCount(ctx, sessionID, s.RequestCount+len(stored)); err != nil {
		// Roll the just-appended entries back so the in-memory store stays
		// consistent with the failed persisted count. The trim above only ever
		// removed entries from the FRONT; the rolled-back tail is the stored
		// entries plus any pre-existing entries the oversized-batch cut took
		// with them — unindex exactly that tail before cutting it.
		tail := m.traffic[sessionID][len(m.traffic[sessionID])-len(stored):]
		m.unindexTraffic(tail)
		m.traffic[sessionID] = m.traffic[sessionID][:len(m.traffic[sessionID])-len(stored)]
		m.trafficMu.Unlock()
		return 0, err
	}
	m.trafficMu.Unlock()

	// M4: a mocked upload is proof the rule was hit; refresh its LastUsedAt so
	// the sliding cleanup window starts over. Heartbeats/polling don't reach here.
	if hits := collectHits(stored); len(hits) > 0 {
		m.touchHitRules(ctx, app, did, hits)
	}
	return len(stored), nil
}

// indexTraffic records the runtime location of each entry in the ID index
// (4.22). Caller must hold the lock guarding the owning structure (trafficMu
// for active sessions, retainedMu for retained ones); this helper takes
// trafficIndexMu internally, keeping the lock order trafficMu → trafficIndexMu
// and retainedMu → trafficIndexMu.
func (m *CaptureManager) indexTraffic(sid string, retained bool, app, did string, entries []*capture.TrafficEntry) {
	m.trafficIndexMu.Lock()
	defer m.trafficIndexMu.Unlock()
	for _, e := range entries {
		if e == nil || e.ID == "" {
			continue
		}
		m.trafficIndex[e.ID] = &trafficIndexEntry{sid: sid, retained: retained, app: app, did: did, entry: e}
	}
}

// unindexTraffic drops index entries for the given entries (4.22). Deleting an
// ID that was never indexed (e.g. trimmed in an earlier step) is a no-op.
func (m *CaptureManager) unindexTraffic(entries []*capture.TrafficEntry) {
	m.trafficIndexMu.Lock()
	defer m.trafficIndexMu.Unlock()
	for _, e := range entries {
		if e == nil || e.ID == "" {
			continue
		}
		delete(m.trafficIndex, e.ID)
	}
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
//
// M2 (O3 48h 保留): an ENDED session stays queryable during its retention
// window (RetainUntil, default 48h from end) — the Web history view reads
// real data from it. Once the window passes (even before the hourly janitor
// runs) the session is reported not found, matching the contract
// "过期清理后 404"; the janitor then releases the memory. An unknown session
// returns ErrSessionNotFound.
func (m *CaptureManager) ListSessionTraffic(ctx context.Context, sessionID string, limit, offset int) ([]*capture.TrafficEntry, int, error) {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, 0, ErrSessionNotFound
		}
		return nil, 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		// O3: expired-ended sessions are gone from the API even if the hourly
		// janitor has not run yet (record purge happens there).
		if s.RetainUntil == nil || time.Now().After(*s.RetainUntil) {
			return nil, 0, ErrSessionNotFound
		}
		// Within the retention window: fall through and return the real data.
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
//
// 4.22: the ID index resolves the entry in O(1) instead of scanning every
// session's entries. The value is copied under the index read lock; entries
// are immutable after upload, so a racing delete/purge that removes the index
// entry afterwards only means this particular lookup saw the entry just before
// its removal (same benign window the slice scans had).
func (m *CaptureManager) GetTraffic(ctx context.Context, id string) (*capture.TrafficEntry, error) {
	m.trafficIndexMu.RLock()
	ie := m.trafficIndex[id]
	if ie == nil {
		m.trafficIndexMu.RUnlock()
		return nil, ErrNotFound
	}
	c := *ie.entry
	m.trafficIndexMu.RUnlock()
	return &c, nil
}

// GetTrafficWithOwner returns the owning device (app, did) of a traffic entry,
// whether the entry lives in an active session or in the retained store of an
// ended session. It is used to authorize share creation after the owning
// session record is gone (M9 deletes it on end). Returns ErrNotFound when the
// entry does not exist.
//
// 4.22: resolved through the ID index. Active entries still resolve the owner
// via the session record under trafficMu (EndSession, which deletes the record
// and flips the index entry to retained, holds trafficMu.Lock for the whole
// sequence — so the read cannot observe "index says active but record gone").
// Retained entries carry the owner snapshot taken at end time.
func (m *CaptureManager) GetTrafficWithOwner(ctx context.Context, id string) (app, did string, err error) {
	m.trafficMu.RLock()
	m.trafficIndexMu.RLock()
	ie := m.trafficIndex[id]
	if ie == nil {
		m.trafficIndexMu.RUnlock()
		m.trafficMu.RUnlock()
		return "", "", ErrNotFound
	}
	if ie.retained {
		app, did = ie.app, ie.did
		m.trafficIndexMu.RUnlock()
		m.trafficMu.RUnlock()
		return app, did, nil
	}
	// Active entry: EndSession (the only path that deletes the session record)
	// is blocked while trafficMu is held, so the record is guaranteed to exist.
	s, err := m.sessions.Get(ctx, ie.sid)
	m.trafficIndexMu.RUnlock()
	m.trafficMu.RUnlock()
	if err != nil {
		return "", "", ErrNotFound
	}
	return s.App, s.Did, nil
}

// DeleteTraffic deletes a single traffic entry by its server-generated ID
// (M9.5: Web per-row "删除"). The entry may live in an active session or in
// the retained store of an ended session (4.11): a deleted session's record
// is gone, but its retained rows are still deletable by the owning device.
// The owning session's persisted RequestCount is decremented when the session
// still exists. An unknown ID reports ErrNotFound — the caller treats delete
// as best-effort idempotent.
func (m *CaptureManager) DeleteTraffic(ctx context.Context, id string) error {
	// Active-session entries live under trafficMu. Deleting by ID from the two
	// maps is two separate critical sections (no nested trafficMu→retainedMu:
	// purgeRetainedBefore takes the opposite order), so a racing EndSession
	// that moves the entry to retained is still caught by the second scan.
	m.trafficMu.Lock()
	found := false
	for sid, entries := range m.traffic {
		for i, e := range entries {
			if e.ID != id {
				continue
			}
			m.traffic[sid] = append(entries[:i], entries[i+1:]...)
			m.unindexTraffic([]*capture.TrafficEntry{e}) // 4.22
			// Keep the persisted session count in sync (best-effort; sessions
			// may already be gone under M9 delete-on-end semantics).
			if s, err := m.sessions.Get(ctx, sid); err == nil && s.RequestCount > 0 {
				s.RequestCount--
				_ = m.sessions.Update(ctx, s)
			}
			found = true
			break
		}
		if found {
			break
		}
	}
	m.trafficMu.Unlock()
	if found {
		return nil
	}

	m.retainedMu.Lock()
	defer m.retainedMu.Unlock()
	for sid, rs := range m.retained {
		for i, e := range rs.Entries {
			if e.ID != id {
				continue
			}
			rs.Entries = append(rs.Entries[:i], rs.Entries[i+1:]...)
			m.unindexTraffic([]*capture.TrafficEntry{e}) // 4.22
			if len(rs.Entries) == 0 {
				delete(m.retained, sid)
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
	// Same serialization as UploadTraffic: the status check and the traffic
	// mutation happen under trafficMu, so a concurrent EndSession cannot
	// interleave between the check and the delete.
	m.trafficMu.Lock()
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		m.trafficMu.Unlock()
		if errors.Is(err, ErrNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	if s.Status == capture.SessionStatusEnded {
		m.trafficMu.Unlock()
		return ErrSessionEnded
	}
	m.unindexTraffic(m.traffic[sessionID]) // 4.22
	delete(m.traffic, sessionID)
	sc := *s
	sc.RequestCount = 0
	if err := m.sessions.Update(ctx, &sc); err != nil {
		m.trafficMu.Unlock()
		return err
	}
	m.trafficMu.Unlock()
	return nil
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

// purgeRetainedBefore removes every retained session ended before cutoff, and
// sweeps orphaned in-memory traffic keys whose owning session no longer exists
// (belt-and-braces for leftovers from pre-fix races; the EndSession/UploadTraffic
// serialization prevents new orphans).
func (m *CaptureManager) purgeRetainedBefore(ctx context.Context, cutoff time.Time) {
	m.retainedMu.Lock()
	defer m.retainedMu.Unlock()
	for sid, rs := range m.retained {
		if rs.EndedAt.Before(cutoff) {
			m.unindexTraffic(rs.Entries) // 4.22
			delete(m.retained, sid)
		}
	}

	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	for sid := range m.traffic {
		if _, err := m.sessions.Get(ctx, sid); err != nil {
			m.unindexTraffic(m.traffic[sid]) // 4.22
			delete(m.traffic, sid)
		}
	}
}
