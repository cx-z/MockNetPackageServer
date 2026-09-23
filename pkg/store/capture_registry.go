package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// Capture errors mapped to API error codes in the admin handlers.
var (
	// ErrDeviceNotRegistered means no device with (App, Did) exists.
	ErrDeviceNotRegistered = errors.New("device not registered")
	// ErrDeviceOffline means the device's last heartbeat exceeded the timeout
	// (activating a capture session for an offline device is rejected).
	ErrDeviceOffline = errors.New("device offline")
	// ErrSessionNotFound means no capture session with the given ID exists.
	ErrSessionNotFound = errors.New("capture session not found")
	// ErrSessionEnded means the capture session is already ended.
	ErrSessionEnded = errors.New("capture session already ended")
	// ErrRuleNotFound means no mock rule with the given ID exists for the device.
	ErrRuleNotFound = errors.New("mock rule not found")
	// ErrRuleConflict means enabling this rule would leave more than one enabled
	// rule on the same interface (maps to HTTP 409).
	ErrRuleConflict = errors.New("mock rule conflict: another enabled rule already exists for this interface")
	// ErrNoteRequired means the PUT actually edits the canned response
	// (body/statusCode/headers changed) but carries a blank note (maps to HTTP 400).
	// A pure toggle (response echoed unchanged) may leave the note blank — rules
	// created from a capture ("Mock 此请求") have no note and must be enableable
	// without forcing an edit (M7).
	ErrNoteRequired = errors.New("mock rule note is required when editing the canned response")
)

// MockRuleConflictMessage is the fixed popup message Web shows when an interface
// falls into the abnormal multi-enabled state (requirement 6.5 / F4.6).
const MockRuleConflictMessage = "不允许同一个接口同时开启多个 Mock 规则"

// CaptureConfig carries the MockNetPack capture runtime configuration.
// All values are server-side configuration items (requirement 决策 #14).
type CaptureConfig struct {
	// HeartbeatInterval is the interval the SDK is advised to heartbeat at.
	HeartbeatInterval time.Duration
	// HeartbeatTimeout is the threshold after which a device is considered
	// offline and its capture session is ended.
	HeartbeatTimeout time.Duration
	// ViewerTTL is the lease TTL granted to Web page viewers; viewers renew
	// periodically and expired leases are garbage-collected.
	ViewerTTL time.Duration
	// MockRuleRetention is how long persisted mock rules (and their source
	// snapshots) are kept since their last use before being purged (M4,
	// F8.3/决策15, sliding window).
	MockRuleRetention time.Duration
	// RetainedTrafficTTL is how long traffic of an ended session stays
	// resolvable by ID for share-link creation (M8.6 断开后可分享). It bounds
	// the in-memory retained store; 0 means the default (7d, same as ShareTTL).
	RetainedTrafficTTL time.Duration
}

// DefaultCaptureConfig returns the default capture configuration
// (heartbeat 20s advised / 60s timeout, viewer lease 120s, rule retention 7d,
// retained-traffic window 7d aligned with share-link TTL).
func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		HeartbeatInterval: 20 * time.Second,
		HeartbeatTimeout:  60 * time.Second,
		ViewerTTL:         120 * time.Second,
		MockRuleRetention: 7 * 24 * time.Hour,
		RetainedTrafficTTL: ShareTTL,
	}
}

// CaptureManager implements the runtime semantics for devices, capture
// sessions and viewer leases on top of the persistent stores. It follows the
// EngineRegistry pattern (store package, no file implementation dependency):
// persistence is delegated to the injected DeviceStore / CaptureSessionStore,
// while viewer leases are runtime-only state (persisting them across restarts
// is meaningless — every lease would be expired).
type CaptureManager struct {
	devices  DeviceStore
	sessions CaptureSessionStore
	rules    MockRuleStore
	cfg      CaptureConfig
	log      *slog.Logger

	// viewerMu guards the runtime viewer leases, keyed by session ID.
	viewerMu sync.RWMutex
	viewers  map[string]map[string]capture.ViewerLease

	// trafficMu guards the runtime traffic entries, keyed by session ID.
	// Traffic is session-scoped temporary data (全量抓包、会话内可见): it lives
	// in memory only, is never persisted, and moves to the retained store when
	// the session ends (M8.6 keeps ended-session records shareable).
	trafficMu sync.RWMutex
	traffic   map[string][]*capture.TrafficEntry

	// retainedMu guards retainedTraffic: the traffic of ended sessions, kept
	// for RetainedTrafficTTL so records the user saw on the page can still be
	// shared after disconnect. M9 list semantics are unchanged — ended sessions
	// are deleted and never listed again; retained entries are reachable only
	// by ID (GetTraffic / share creation).
	retainedMu sync.RWMutex
	retained   map[string]*retainedSession

	// sharesMu guards the share snapshots (M8.5). Shares are independent copies
	// of a single traffic entry, decoupled from the owning session/traffic —
	// clearing the session does not invalidate the share. TTL 7 days.
	sharesMu sync.RWMutex
	shares   map[string]*ShareSnapshot

	ctx      context.Context
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewCaptureManager creates a capture manager backed by the given stores.
func NewCaptureManager(devices DeviceStore, sessions CaptureSessionStore, rules MockRuleStore, cfg CaptureConfig) *CaptureManager {
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = DefaultCaptureConfig().HeartbeatTimeout
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultCaptureConfig().HeartbeatInterval
	}
	if cfg.ViewerTTL <= 0 {
		cfg.ViewerTTL = DefaultCaptureConfig().ViewerTTL
	}
	if cfg.MockRuleRetention <= 0 {
		cfg.MockRuleRetention = DefaultCaptureConfig().MockRuleRetention
	}
	if cfg.RetainedTrafficTTL <= 0 {
		cfg.RetainedTrafficTTL = DefaultCaptureConfig().RetainedTrafficTTL
	}
	return &CaptureManager{
		devices:  devices,
		sessions: sessions,
		rules:    rules,
		cfg:      cfg,
		log:      slog.Default(),
		viewers:  make(map[string]map[string]capture.ViewerLease),
		traffic:  make(map[string][]*capture.TrafficEntry),
		retained: make(map[string]*retainedSession),
		shares:   make(map[string]*ShareSnapshot),
		stopCh:   make(chan struct{}),
	}
}

// SetLogger sets the logger used for background health-check warnings.
func (m *CaptureManager) SetLogger(log *slog.Logger) {
	if log != nil {
		m.log = log
	}
}

// Config returns the runtime capture configuration.
func (m *CaptureManager) Config() CaptureConfig {
	return m.cfg
}

// ServerConfig returns the SDK-facing server configuration (heartbeat
// interval / timeout in seconds) pushed to devices at registration/heartbeat.
func (m *CaptureManager) ServerConfig() capture.ServerConfig {
	return capture.ServerConfig{
		HeartbeatIntervalSeconds: int(m.cfg.HeartbeatInterval.Seconds()),
		HeartbeatTimeoutSeconds:  int(m.cfg.HeartbeatTimeout.Seconds()),
	}
}

// ============================================================================
// Devices
// ============================================================================

// CreateManualDevice creates a device entry from the Web UI (M7.2.1): it does
// NOT upsert — an existing (App, Did) returns ErrAlreadyExists so the Web can
// report a conflict. The caller (handler) stamps Owner and Name; this is the
// only device-creation path once M7.2.3 stops SDK auto-registration.
func (m *CaptureManager) CreateManualDevice(ctx context.Context, d *capture.Device) (*capture.Device, error) {
	if _, err := m.devices.Get(ctx, d.App, d.Did); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := time.Now()
	d.RegisteredAt = now
	d.LastSeenAt = now
	if err := m.devices.Create(ctx, d); err != nil {
		return nil, err
	}
	out := *d
	return &out, nil
}

// RegisterDevice registers or re-registers a device (idempotent upsert on
// (App, Did)). A re-registration refreshes metadata and marks the device
// active (LastSeenAt = now). Registration is only accepted for a device
// that already exists OR is new; nothing is rejected here — offline devices
// come back online on their next register/heartbeat.
// UpdateDeviceName changes a device's display name (M7.2.2). Returns
// ErrNotFound when the (app, did) does not exist; ownership is checked by the
// admin handler layer before calling this.
func (m *CaptureManager) UpdateDeviceName(ctx context.Context, app, did, name string) (*capture.Device, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		return nil, err
	}
	d.Name = name
	if err := m.devices.Update(ctx, d); err != nil {
		return nil, err
	}
	out := *d
	return &out, nil
}

func (m *CaptureManager) RegisterDevice(ctx context.Context, d *capture.Device) (*capture.Device, error) {
	now := time.Now()
	d.LastSeenAt = now

	existing, err := m.devices.Get(ctx, d.App, d.Did)
	if errors.Is(err, ErrNotFound) {
		d.RegisteredAt = now
		if err := m.devices.Create(ctx, d); err != nil {
			return nil, err
		}
		out := *d
		return &out, nil
	}
	if err != nil {
		return nil, err
	}

	// Upsert: keep the original registration time, refresh metadata.
	existing.OSVersion = d.OSVersion
	existing.SDKVersion = d.SDKVersion
	existing.AppVersion = d.AppVersion
	existing.Platform = d.Platform
	existing.LastSeenAt = now
	if err := m.devices.Update(ctx, existing); err != nil {
		return nil, err
	}
	out := *existing
	return &out, nil
}

// Heartbeat refreshes the device's last-seen time and returns the device plus
// its active capture session (nil if none). It is the SDK's keep-alive and
// the session-state channel (the SDK starts/stops capture based on the
// returned session).
func (m *CaptureManager) Heartbeat(ctx context.Context, app, did string) (*capture.Device, *capture.CaptureSession, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrDeviceNotRegistered
		}
		return nil, nil, err
	}

	d.LastSeenAt = time.Now()
	if err := m.devices.Update(ctx, d); err != nil {
		return nil, nil, err
	}

	session, err := m.activeSessionFor(ctx, app, did)
	if err != nil {
		return nil, nil, err
	}
	out := *d
	return &out, session, nil
}

// ListDevices returns all devices with derived status and current session.
func (m *CaptureManager) ListDevices(ctx context.Context, filter *DeviceFilter) ([]*capture.DeviceView, error) {
	devices, err := m.devices.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	// One query for all active sessions, grouped by (app, did).
	activeByDevice := make(map[string]*capture.CaptureSession)
	active, err := m.sessions.List(ctx, &SessionFilter{Status: &activeStatus})
	if err != nil {
		return nil, err
	}
	for _, s := range active {
		key := s.App + "\x00" + s.Did
		if _, exists := activeByDevice[key]; !exists {
			activeByDevice[key] = s
		}
	}

	now := time.Now()
	views := make([]*capture.DeviceView, 0, len(devices))
	for _, d := range devices {
		session := activeByDevice[d.App+"\x00"+d.Did]
		var sessionCopy *capture.CaptureSession
		if session != nil {
			sc := *session
			sessionCopy = &sc
		}
		dc := *d
		views = append(views, &capture.DeviceView{
			Device:         &dc,
			Status:         capture.DeriveDeviceStatus(d.LastSeenAt, now, m.cfg.HeartbeatTimeout, session != nil),
			CurrentSession: sessionCopy,
		})
	}
	return views, nil
}

// GetDevice returns a single device with derived status and current session.
func (m *CaptureManager) GetDevice(ctx context.Context, app, did string) (*capture.DeviceView, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrDeviceNotRegistered
		}
		return nil, err
	}

	session, err := m.activeSessionFor(ctx, app, did)
	if err != nil {
		return nil, err
	}

	var sessionCopy *capture.CaptureSession
	if session != nil {
		sc := *session
		sessionCopy = &sc
	}
	dc := *d
	return &capture.DeviceView{
		Device:         &dc,
		Status:         capture.DeriveDeviceStatus(d.LastSeenAt, time.Now(), m.cfg.HeartbeatTimeout, session != nil),
		CurrentSession: sessionCopy,
	}, nil
}

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

// ============================================================================
// Mock rules (M3)
// ============================================================================

// interfaceKey identifies a mockable endpoint by Method + URL path (Query/Body
// do not participate in matching, requirement 决策 #19).
type interfaceKey struct {
	method string
	path   string
}

// enabledOnInterface returns the rules of (app, did) that are enabled and match
// (method, path), excluding excludeID (used when the rule being edited is the
// existing row).
func (m *CaptureManager) enabledOnInterface(ctx context.Context, app, did, method, path, excludeID string) ([]*capture.MockRule, error) {
	all, err := m.rules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		return nil, err
	}
	var out []*capture.MockRule
	for _, r := range all {
		if r.ID == excludeID {
			continue
		}
		if r.Enabled && r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out, nil
}

// evaluateRules recomputes the runtime Effective flag for every rule of a device
// and collects the abnormal multi-enabled interfaces. A rule is Effective only
// when it is the sole enabled rule on its interface; an interface with >1
// enabled rule mocks nothing and is reported as a conflict.
func (m *CaptureManager) evaluateRules(ctx context.Context, app, did string) ([]*capture.MockRuleView, []capture.MockRuleConflict, int, error) {
	all, err := m.rules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		return nil, nil, 0, err
	}
	version, err := m.rules.GetRuleVersion(ctx, app, did)
	if err != nil {
		return nil, nil, 0, err
	}

	enabledCount := make(map[interfaceKey]int)
	for _, r := range all {
		if r.Enabled {
			enabledCount[interfaceKey{r.Method, r.Path}]++
		}
	}

	views := make([]*capture.MockRuleView, 0, len(all))
	for _, r := range all {
		eff := r.Enabled && enabledCount[interfaceKey{r.Method, r.Path}] == 1
		c := *r
		views = append(views, &capture.MockRuleView{MockRule: &c, Effective: eff})
	}

	var conflicts []capture.MockRuleConflict
	for k, n := range enabledCount {
		if n > 1 {
			conflicts = append(conflicts, capture.MockRuleConflict{
				Method:  k.method,
				Path:    k.path,
				Message: MockRuleConflictMessage,
			})
		}
	}
	return views, conflicts, version, nil
}

// CreateMockRule persists a new rule. When enabled=true it enforces the
// single-active rule per interface: if another enabled rule already matches the
// same Method+Path it returns ErrRuleConflict (409). Every write bumps the
// device rule-set version.
func (m *CaptureManager) CreateMockRule(ctx context.Context, app, did string, in *capture.MockRuleInput) (*capture.MockRuleView, int, error) {
	now := time.Now()
	if in.Enabled {
		others, err := m.enabledOnInterface(ctx, app, did, in.Method, in.Path, "")
		if err != nil {
			return nil, 0, err
		}
		if len(others) > 0 {
			return nil, 0, ErrRuleConflict
		}
	}
	rule := &capture.MockRule{
		ID:         id.ULID(),
		App:        app,
		Did:        did,
		Method:     in.Method,
		Path:       in.Path,
		Response:   in.Response,
		Enabled:    in.Enabled,
		Note:       in.Note,
		Source:     in.Source,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastUsedAt: now,
	}
	if err := m.rules.Create(ctx, rule); err != nil {
		return nil, 0, err
	}
	version, err := m.rules.BumpRuleVersion(ctx, app, did)
	if err != nil {
		return nil, 0, err
	}
	views, _, _, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, version, err
	}
	for _, v := range views {
		if v.ID == rule.ID {
			return v, version, nil
		}
	}
	return &capture.MockRuleView{MockRule: rule, Effective: in.Enabled}, version, nil
}

// UpdateMockRule edits a rule's canned response, note, and/or enabled switch
// (M5). The match key (Method+Path) and the source snapshot are immutable —
// the input type UpdateMockRuleInput deliberately omits them. Turning the switch
// on is rejected with ErrRuleConflict if another enabled rule already matches
// the rule's (frozen) interface. An absent Enabled pointer leaves the current
// switch untouched. Writes bump the rule-set version and refresh LastUsedAt.
func (m *CaptureManager) UpdateMockRule(ctx context.Context, app, did, ruleID string, in *capture.UpdateMockRuleInput) (*capture.MockRuleView, int, error) {
	existing, err := m.rules.Get(ctx, ruleID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, 0, ErrRuleNotFound
		}
		return nil, 0, err
	}
	if existing.App != app || existing.Did != did {
		return nil, 0, ErrRuleNotFound
	}

	// M7: note is required only when the PUT actually edits the canned
	// response (statusCode/headers/body changed). A pure toggle echoes the
	// stored response unchanged and may leave the note blank — rules created
	// from a capture ("Mock 此请求") carry no note and enabling them must not
	// force an edit.
	if !reflect.DeepEqual(existing.Response, in.Response) && strings.TrimSpace(in.Note) == "" {
		return nil, 0, ErrNoteRequired
	}

	// Enforce single-active on the frozen interface when the edit turns the
	// rule on (Enabled pointer present and true, while currently off).
	if in.Enabled != nil && *in.Enabled && !existing.Enabled {
		others, err := m.enabledOnInterface(ctx, app, did, existing.Method, existing.Path, ruleID)
		if err != nil {
			return nil, 0, err
		}
		if len(others) > 0 {
			return nil, 0, ErrRuleConflict
		}
	}

	existing.Response = in.Response
	existing.Note = in.Note
	if in.Enabled != nil {
		existing.Enabled = *in.Enabled
	}
	existing.UpdatedAt = time.Now()
	// Editing a rule or toggling it counts as "used" (M4 sliding window).
	existing.LastUsedAt = existing.UpdatedAt
	if err := m.rules.Update(ctx, existing); err != nil {
		return nil, 0, err
	}
	version, err := m.rules.BumpRuleVersion(ctx, app, did)
	if err != nil {
		return nil, 0, err
	}
	views, _, _, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, version, err
	}
	for _, v := range views {
		if v.ID == ruleID {
			return v, version, nil
		}
	}
	return nil, version, ErrRuleNotFound
}

// DeleteMockRule removes a rule by ID (scoped to app/did). Writes bump the
// rule-set version so the SDK drops it from its local snapshot.
func (m *CaptureManager) DeleteMockRule(ctx context.Context, app, did, ruleID string) (int, error) {
	existing, err := m.rules.Get(ctx, ruleID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrRuleNotFound
		}
		return 0, err
	}
	if existing.App != app || existing.Did != did {
		return 0, ErrRuleNotFound
	}
	if err := m.rules.Delete(ctx, ruleID); err != nil {
		return 0, err
	}
	return m.rules.BumpRuleVersion(ctx, app, did)
}

// ListMockRules returns every rule of the device (Web view, including disabled)
// with the runtime Effective flag, the abnormal conflicts, and the current
// rule-set version.
func (m *CaptureManager) ListMockRules(ctx context.Context, app, did string) ([]*capture.MockRuleView, []capture.MockRuleConflict, int, error) {
	return m.evaluateRules(ctx, app, did)
}

// ListActiveMockRules is the SDK pull. When the device rule-set version equals
// sinceVersion nothing changed: it returns no rules and changed=false. Otherwise
// it returns only the Effective rules (abnormal interfaces are excluded
// entirely) and the new version.
func (m *CaptureManager) ListActiveMockRules(ctx context.Context, app, did string, sinceVersion int) ([]*capture.MockRuleView, int, bool, error) {
	views, _, version, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, 0, false, err
	}
	if version == sinceVersion {
		return nil, version, false, nil
	}
	active := make([]*capture.MockRuleView, 0, len(views))
	for _, v := range views {
		if v.Effective {
			active = append(active, v)
		}
	}
	return active, version, true, nil
}

// RuleVersion returns the current monotonic rule-set version for (app, did).
// Used by the device heartbeat to tell the SDK whether it should pull an
// updated rule snapshot.
func (m *CaptureManager) RuleVersion(ctx context.Context, app, did string) (int, error) {
	return m.rules.GetRuleVersion(ctx, app, did)
}

// ============================================================================
// Rule retention janitor (M4)
// ============================================================================

// ruleUsageTime picks the sliding-window baseline for a rule: LastUsedAt,
// falling back to UpdatedAt (and then CreatedAt) for legacy rows that predate
// M4 and have no LastUsedAt persisted yet.
func ruleUsageTime(r *capture.MockRule) time.Time {
	if !r.LastUsedAt.IsZero() {
		return r.LastUsedAt
	}
	if !r.UpdatedAt.IsZero() {
		return r.UpdatedAt
	}
	return r.CreatedAt
}

// PurgeExpiredRules deletes every mock rule whose last use is older than the
// configured retention (default 7d, sliding window). A deleted rule bumps its
// device rule-set version so the SDK drops it from its local snapshot on the
// next heartbeat. Best-effort: per-rule errors are logged, not fatal.
func (m *CaptureManager) PurgeExpiredRules(ctx context.Context) {
	if m.cfg.MockRuleRetention <= 0 {
		return
	}
	all, err := m.rules.List(ctx, nil)
	if err != nil {
		m.log.Warn("rule janitor: list rules failed", "error", err)
		return
	}
	now := time.Now()
	bumped := make(map[string]bool) // (app\0did) already bumped
	for _, r := range all {
		if now.Sub(ruleUsageTime(r)) <= m.cfg.MockRuleRetention {
			continue
		}
		if err := m.rules.Delete(ctx, r.ID); err != nil {
			m.log.Warn("rule janitor: delete expired rule failed", "rule", r.ID, "error", err)
			continue
		}
		key := r.App + "\x00" + r.Did
		if !bumped[key] {
			if _, err := m.rules.BumpRuleVersion(ctx, r.App, r.Did); err != nil {
				m.log.Warn("rule janitor: bump version failed", "app", r.App, "did", r.Did, "error", err)
			}
			bumped[key] = true
		}
		m.log.Info("rule janitor: purged expired mock rule",
			"rule", r.ID, "method", r.Method, "path", r.Path, "lastUsedAt", ruleUsageTime(r))
	}
}

// disableDeviceRules flips every enabled mock rule of a device off when its
// capture session ends (M4, F4.5/决策13). Rules are NOT deleted — they remain
// in the Web rule history — but they are no longer effective; on the next
// capture session the user must re-enable each one manually. The rule-set
// version is bumped so the SDK drops them from its local snapshot.
func (m *CaptureManager) disableDeviceRules(ctx context.Context, app, did string) {
	all, err := m.rules.List(ctx, &MockRuleFilter{App: app, Did: did})
	if err != nil {
		m.log.Warn("session end: list rules to disable failed", "error", err)
		return
	}
	changed := false
	for _, r := range all {
		if !r.Enabled {
			continue
		}
		r.Enabled = false
		if err := m.rules.Update(ctx, r); err != nil {
			m.log.Warn("session end: disable rule failed", "rule", r.ID, "error", err)
			continue
		}
		changed = true
	}
	if changed {
		if _, err := m.rules.BumpRuleVersion(ctx, app, did); err != nil {
			m.log.Warn("session end: bump rule version failed", "app", app, "did", did, "error", err)
		}
	}
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
	ShareID   string                 `json:"shareId"`
	CreatedAt time.Time              `json:"createdAt"`
	ExpiresAt time.Time              `json:"expiresAt"`
	Entry     *capture.TrafficEntry  `json:"entry"`
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

	m.sharesMu.Lock()
	m.shares[snap.ShareID] = snap
	m.sharesMu.Unlock()

	return snap, nil
}

// GetShare returns a share snapshot by ID, or ErrNotFound if it does not
// exist or has expired. Expired shares are lazily purged on access.
func (m *CaptureManager) GetShare(ctx context.Context, shareID string) (*ShareSnapshot, error) {
	m.sharesMu.RLock()
	snap, ok := m.shares[shareID]
	m.sharesMu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if time.Now().After(snap.ExpiresAt) {
		m.sharesMu.Lock()
		delete(m.shares, shareID)
		m.sharesMu.Unlock()
		return nil, ErrNotFound
	}
	c := *snap
	return &c, nil
}

// DeleteDevice removes a device by (App, Did) and all its associated mock
// rules and active sessions.
func (m *CaptureManager) DeleteDevice(ctx context.Context, app, did string) error {
	// End active sessions for this device (if any)
	if sessions, err := m.sessions.List(ctx, &SessionFilter{App: &app, Did: &did}); err == nil {
		for _, s := range sessions {
			if s.Status == capture.SessionStatusCapturing {
				_ = m.EndSession(ctx, s.ID)
			}
		}
	}
	// Delete associated mock rules
	views, _, _, err := m.ListMockRules(ctx, app, did)
	if err != nil && !errors.Is(err, ErrRuleNotFound) {
		return err
	}
	for _, v := range views {
		_, _ = m.DeleteMockRule(ctx, app, did, v.ID)
	}
	return m.devices.Delete(ctx, app, did)
}
