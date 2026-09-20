package store

import (
	"context"
	"errors"
	"log/slog"
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
}

// DefaultCaptureConfig returns the default capture configuration
// (heartbeat 20s advised / 60s timeout, viewer lease 120s).
func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		HeartbeatInterval: 20 * time.Second,
		HeartbeatTimeout:  60 * time.Second,
		ViewerTTL:         120 * time.Second,
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
	// in memory only, is cleared when the session ends, and is never persisted
	// in M2 (mocked-request persistence lands in M3).
	trafficMu sync.RWMutex
	traffic   map[string][]*capture.TrafficEntry

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
	return &CaptureManager{
		devices:  devices,
		sessions: sessions,
		rules:    rules,
		cfg:      cfg,
		log:      slog.Default(),
		viewers:  make(map[string]map[string]capture.ViewerLease),
		traffic:  make(map[string][]*capture.TrafficEntry),
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

// RegisterDevice registers or re-registers a device (idempotent upsert on
// (App, Did)). A re-registration refreshes metadata and marks the device
// active (LastSeenAt = now). Registration is only accepted for a device
// that already exists OR is new; nothing is rejected here — offline devices
// come back online on their next register/heartbeat.
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

	// Clear the session's temporary traffic: a session that has ended no
	// longer exposes its traffic (contract: ended session => empty list).
	m.trafficMu.Lock()
	delete(m.traffic, id)
	m.trafficMu.Unlock()

	sc := *s
	sc.End(time.Now())
	sc.ViewerCount = 0
	return m.sessions.Update(ctx, &sc)
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
	return len(stored), nil
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
// traffic of an ended session has been cleared, so such entries are reported
// as not found (contract: 404 not_found).
func (m *CaptureManager) GetTraffic(ctx context.Context, id string) (*capture.TrafficEntry, error) {
	m.trafficMu.RLock()
	defer m.trafficMu.RUnlock()
	for _, entries := range m.traffic {
		for _, e := range entries {
			if e.ID == id {
				c := *e
				return &c, nil
			}
		}
	}
	return nil, ErrNotFound
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

	sc := *s
	sc.ViewerCount = count
	if count == 0 && sc.Status == capture.SessionStatusCapturing {
		sc.End(time.Now())
	}
	return m.sessions.Update(ctx, &sc)
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

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.checkDeviceHealth(ctx)
				m.checkViewerLeases(ctx)
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
		sc := *s
		sc.ViewerCount = 0
		sc.End(now)
		if err := m.sessions.Update(ctx, &sc); err != nil {
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
		ID:        id.ULID(),
		App:       app,
		Did:       did,
		Method:    in.Method,
		Path:      in.Path,
		Response:  in.Response,
		Enabled:   in.Enabled,
		Source:    in.Source,
		CreatedAt: now,
		UpdatedAt: now,
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

// UpdateMockRule edits a rule's content and/or enabled switch. Turning the
// switch on is rejected with ErrRuleConflict if another enabled rule already
// matches the (possibly new) Method+Path. Writes bump the rule-set version.
func (m *CaptureManager) UpdateMockRule(ctx context.Context, app, did, ruleID string, in *capture.MockRuleInput) (*capture.MockRuleView, int, error) {
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

	// Enforce single-active on the (possibly new) interface when enabling.
	if in.Enabled && !existing.Enabled {
		others, err := m.enabledOnInterface(ctx, app, did, in.Method, in.Path, ruleID)
		if err != nil {
			return nil, 0, err
		}
		if len(others) > 0 {
			return nil, 0, ErrRuleConflict
		}
	}

	existing.Method = in.Method
	existing.Path = in.Path
	existing.Response = in.Response
	existing.Enabled = in.Enabled
	existing.Source = in.Source
	existing.UpdatedAt = time.Now()
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
