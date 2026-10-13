package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: longconn_registry.go
// Session-scoped long-connection logs (能力线 L1, 业务级日志采集): upload,
// persist. 独立于 HTTP 流量——独立额度、独立存档（mockd-data/longconn）、
// 独立 seq 计数，不占用 traffic 的内存与上限。
// ============================================================================

// DefaultMaxSessionLongConnEntries is the default cap on the number of
// long-connection log entries kept per session (决策: 独立额度 1000 条).
// 超限丢最旧（rolling window），与 traffic 窗口同语义。
// Overridable via --capture-session-longconn-max-entries
// (CaptureConfig.MaxSessionLongConnEntries).
const DefaultMaxSessionLongConnEntries = 1000

// UploadLongConn appends a batch of long-connection records to a capturing
// session (contract POST /sessions/{id}/longconn). Only records for an active
// session are accepted: an unknown session returns ErrSessionNotFound, an
// ended session returns ErrSessionEnded. Session attribution comes from the
// request path — the upload body carries no app/did.
//
// Partially-accepted semantics: entries failing Normalize+Validate are dropped
// silently (the SDK bridge should already have filtered, but server-side
// validation is authoritative); the returned count is the number of records
// actually stored. IDs and Seq are server-generated here, exactly like
// UploadTraffic, but longconn keeps its OWN seq counter — longconn and traffic
// never share a counter (决策: 独立计数、互不跳号).
//
// Long-connection logs do NOT count against MaxSessionTrafficEntries and do
// NOT touch RequestCount (独立额度, 不占用 HTTP 流量额度).
func (m *CaptureManager) UploadLongConn(ctx context.Context, sessionID string, req capture.LongConnUploadRequest) (int, error) {
	stored := make([]*capture.LongConnectionRecord, 0, len(req.Records))
	for i := range req.Records {
		in := req.Records[i]
		in.Normalize()
		if err := in.Validate(); err != nil {
			continue
		}
		rec := &capture.LongConnectionRecord{
			ID:                        id.ULID(),
			SessionID:                 sessionID,
			LongConnectionRecordInput: in,
		}
		stored = append(stored, rec)
	}
	if len(stored) == 0 {
		return 0, nil
	}

	// Same authoritative-inside-lock rationale as UploadTraffic: EndSession
	// holds the same lock across its status check + session delete, so an
	// upload either lands entirely before the end or is rejected after it.
	m.longconnMu.Lock()
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		m.longconnMu.Unlock()
		if errors.Is(err, ErrNotFound) {
			return 0, ErrSessionNotFound
		}
		return 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		m.longconnMu.Unlock()
		return 0, ErrSessionEnded
	}
	// Per-session monotonic sequence numbers stamped at upload time; the SDK
	// never sends Seq. Independent from the traffic counter.
	seq := m.longconnSeq[sessionID]
	for _, rec := range stored {
		seq++
		rec.Seq = seq
	}
	m.longconnSeq[sessionID] = seq
	m.longconn[sessionID] = append(m.longconn[sessionID], stored...)
	// Snapshot 是"连接状态"而非日志：同会话只保留最新一条，既不干扰日志流，
	// 也几乎不占用 1000 条独立额度（滚动窗口按总条数计，最新快照占 1 槽位，
	// 有效日志容量为 999——数值影响可忽略）。
	m.longconn[sessionID] = pruneSnapshots(m.longconn[sessionID])
	// Rolling window: overflow drops the oldest entries (独立上限).
	if n := len(m.longconn[sessionID]); n > m.cfg.MaxSessionLongConnEntries {
		drop := n - m.cfg.MaxSessionLongConnEntries
		m.longconn[sessionID] = append([]*capture.LongConnectionRecord(nil), m.longconn[sessionID][drop:]...)
	}
	// Best-effort persist (errors logged, upload unaffected) — inside the lock
	// so the file always matches the in-memory slice.
	m.persistLongConn(ctx, sessionID)
	m.longconnMu.Unlock()
	return len(stored), nil
}

// pruneSnapshots keeps only the latest snapshot in a record slice. Snapshots
// are connection state (shown in the top status card), not log entries —
// they must not flood the timeline or consume the per-session quota.
func pruneSnapshots(list []*capture.LongConnectionRecord) []*capture.LongConnectionRecord {
	snapCount := 0
	lastIdx := -1
	for i, r := range list {
		if r.EventType == capture.LongConnectionEventSnapshot {
			snapCount++
			lastIdx = i
		}
	}
	if snapCount <= 1 {
		return list
	}
	kept := list[:0]
	for i, r := range list {
		if r.EventType == capture.LongConnectionEventSnapshot && i != lastIdx {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// persistLongConn best-effort writes a session's current longconn slice as its
// archive file. Caller must hold longconnMu (write). A nil longconnStore keeps
// the memory-only behavior. Save errors are logged, never propagated.
func (m *CaptureManager) persistLongConn(ctx context.Context, sessionID string) {
	if m.longconnStore == nil {
		return
	}
	cur := m.longconn[sessionID]
	snapshot := append([]*capture.LongConnectionRecord(nil), cur...)
	if err := m.longconnStore.SaveSessionLongConn(ctx, sessionID, snapshot); err != nil {
		m.log.Warn("longconn archive: persist session longconn failed", "session", sessionID, "error", err)
	}
}

// deleteSessionLongConn best-effort removes a session's longconn archive file
// (session ended / cleared / expired). Errors are logged, never propagated.
func (m *CaptureManager) deleteSessionLongConn(ctx context.Context, sessionID string) {
	if m.longconnStore == nil {
		return
	}
	if err := m.longconnStore.DeleteSessionLongConn(ctx, sessionID); err != nil {
		m.log.Warn("longconn archive: delete session longconn failed", "session", sessionID, "error", err)
	}
}

// ListSessionLongConn returns a session's longconn records for the Web tab
// (contract GET /sessions/{id}/longconn). Session semantics mirror
// ListSessionTraffic: unknown session → ErrSessionNotFound; an ended session
// outside its retention window (RetainUntil) → ErrSessionNotFound; an ended
// session still inside the window returns the real records.
//
// Filter first, then page: filters (channelId/eventType/bizType exact ANDed,
// from/to inclusive, since=seq cursor) narrow the set, total is the filtered
// size, offset/limit page within it. Results are returned in seq ascending
// order (arrival order — the in-memory slice is appended in upload order, so
// no re-sort is needed).
func (m *CaptureManager) ListSessionLongConn(ctx context.Context, sessionID string, q capture.LongConnectionQuery) ([]*capture.LongConnectionRecord, int, error) {
	s, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, 0, ErrSessionNotFound
		}
		return nil, 0, err
	}
	if s.Status == capture.SessionStatusEnded {
		if s.RetainUntil == nil || time.Now().After(*s.RetainUntil) {
			return nil, 0, ErrSessionNotFound
		}
	}

	m.longconnMu.RLock()
	entries := m.longconn[sessionID]
	m.longconnMu.RUnlock()

	filtered := entries
	if q.ChannelID != "" || q.EventType != "" || q.BizType != "" || q.From != nil || q.To != nil || q.Since > 0 || q.Keyword != "" {
		kw := strings.ToLower(q.Keyword)
		filtered = make([]*capture.LongConnectionRecord, 0, len(entries))
		for _, rec := range entries {
			if q.ChannelID != "" && rec.ChannelID != q.ChannelID {
				continue
			}
			if q.EventType != "" && rec.EventType != q.EventType {
				continue
			}
			if q.BizType != "" && rec.BizType != q.BizType {
				continue
			}
			if q.From != nil && rec.TS.Before(*q.From) {
				continue
			}
			if q.To != nil && rec.TS.After(*q.To) {
				continue
			}
			if q.Since > 0 && rec.Seq <= q.Since {
				continue
			}
			if kw != "" {
				hay := strings.ToLower(rec.MsgID + " " + rec.BizType + " " +
					string(rec.EventType) + " " + rec.ChannelID)
				if !strings.Contains(hay, kw) {
					continue
				}
			}
			filtered = append(filtered, rec)
		}
	}

	total := len(filtered)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if q.Limit > 0 && offset+q.Limit < end {
		end = offset + q.Limit
	}
	out := make([]*capture.LongConnectionRecord, 0, end-offset)
	for _, rec := range filtered[offset:end] {
		c := *rec
		out = append(out, &c)
	}
	return out, total, nil
}

// GetLongConnWithOwner returns the owning device (app, did) of a long-
// connection record. Used to authorize share creation (F2 alignment with
// traffic shares): caller must prove device ownership before snapshotting a
// record into a public share. Returns ErrNotFound if the record is gone.
func (m *CaptureManager) GetLongConnWithOwner(ctx context.Context, longConnID string) (app, did string, err error) {
	m.longconnMu.RLock()
	var foundSID string
	for sid, list := range m.longconn {
		for _, rec := range list {
			if rec.ID == longConnID {
				foundSID = sid
				break
			}
		}
		if foundSID != "" {
			break
		}
	}
	m.longconnMu.RUnlock()
	if foundSID == "" {
		return "", "", ErrNotFound
	}
	s, err := m.sessions.Get(ctx, foundSID)
	if err != nil {
		return "", "", ErrNotFound
	}
	return s.App, s.Did, nil
}

// RestoreLongConn loads persisted per-session longconn archives back into
// memory at startup (能力线 L1: 独立存档跨重启保留). Sessions whose record no
// longer exists (cleared/deleted) or whose 48h retention window has already
// passed are skipped and their archive files removed — the same semantics the
// hourly janitor applies. A nil longconnStore is a no-op. Best-effort: load
// errors are logged, never propagated.
func (m *CaptureManager) RestoreLongConn(ctx context.Context) {
	if m.longconnStore == nil {
		return
	}
	all, err := m.longconnStore.LoadAllSessionLongConn(ctx)
	if err != nil {
		m.log.Warn("longconn restore: load persisted archives failed", "error", err)
		return
	}
	now := time.Now()
	for sid, entries := range all {
		s, err := m.sessions.Get(ctx, sid)
		if err != nil {
			m.deleteSessionLongConn(ctx, sid)
			continue
		}
		if s.Status == capture.SessionStatusEnded && (s.RetainUntil == nil || now.After(*s.RetainUntil)) {
			m.deleteSessionLongConn(ctx, sid)
			continue
		}
		if len(entries) == 0 {
			continue
		}
		m.longconnMu.Lock()
		m.longconn[sid] = pruneSnapshots(entries)
		var maxSeq int64
		for _, rec := range entries {
			if rec != nil && rec.Seq > maxSeq {
				maxSeq = rec.Seq
			}
		}
		m.longconnSeq[sid] = maxSeq
		m.longconnMu.Unlock()
	}
}
