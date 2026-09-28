package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/getmockd/mockd/pkg/account"
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
	// ErrPairingTokenInvalid means the presented QR pairing token does not
	// exist, has expired, or is bound to a different app (M9; maps to HTTP 403
	// pairing_token_invalid — the SDK surfaces "二维码已过期，请刷新").
	ErrPairingTokenInvalid = errors.New("pairing token invalid or expired")
)

// MockRuleConflictMessage is the fixed popup message Web shows when an interface
// falls into the abnormal multi-enabled state (requirement 6.5 / F4.6).
const MockRuleConflictMessage = "不允许同一个接口同时开启多个 Mock 规则"

// RuleCaller describes the authenticated user performing a rule mutation
// (O4 权限与分享). nil means --no-auth smoke mode: no session user, full
// access (permission checks are skipped, matching requireAuth bypass).
type RuleCaller struct {
	Username string
	IsAdmin  bool
}

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
	// MaxSessionTrafficEntries caps the number of traffic entries kept per
	// session (O2.1, contract: 超限丢最旧 rolling window). 0 means the server
	// default (DefaultMaxSessionTrafficEntries = 20000).
	MaxSessionTrafficEntries int
	// TrafficRetention is how long an ended session and its traffic stay
	// queryable before the janitor purges them (O3 48h 保留, from session end).
	// 0 means the server default (48h).
	TrafficRetention time.Duration
}

// DefaultCaptureConfig returns the default capture configuration
// (heartbeat 20s advised / 60s timeout, viewer lease 120s, rule retention 7d,
// retained-traffic window 7d aligned with share-link TTL, session traffic cap
// 20000 entries, ended-session traffic retention 48h).
func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		HeartbeatInterval:        20 * time.Second,
		HeartbeatTimeout:         60 * time.Second,
		ViewerTTL:                120 * time.Second,
		MockRuleRetention:        7 * 24 * time.Hour,
		RetainedTrafficTTL:       ShareTTL,
		MaxSessionTrafficEntries: DefaultMaxSessionTrafficEntries,
		TrafficRetention:         DefaultTrafficRetention,
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
	// pairingTokens persists QR pairing tokens (M9): short-lived credentials
	// that let a scanned SDK register a device under the issuing user.
	pairingTokens PairingTokenStore
	// sharesStore persists request share snapshots (M8.5, 4.8): independent
	// copies of a single traffic entry. Persisting them (instead of a
	// memory-only map) keeps a share link valid for its full 7-day TTL across
	// server restarts.
	sharesStore ShareStore
	cfg         CaptureConfig
	log         *slog.Logger
	dataFile    string // O2.4 存储水位：data.json 绝对路径（空=不输出水位日志）

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

	// trafficIndexMu guards trafficIndex (4.22): an ID → location map so
	// GetTraffic / GetTrafficWithOwner / DeleteTraffic resolve by ID in O(1)
	// instead of scanning every session's entries. Entries are created on
	// upload, flipped to retained when the session ends, and removed on
	// delete/clear/purge — always inside the same critical section that
	// mutates the owning slice. Lock order: trafficMu → trafficIndexMu and
	// retainedMu → trafficIndexMu (never the reverse).
	trafficIndexMu sync.RWMutex
	trafficIndex   map[string]*trafficIndexEntry

	// sharesMu serializes share snapshot access (M8.5). The snapshots
	// themselves live in sharesStore (persisted); the mutex guards the
	// read-expire-delete compound in GetShare.
	sharesMu sync.RWMutex

	ctx      context.Context
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewCaptureManager creates a capture manager backed by the given stores.
func NewCaptureManager(devices DeviceStore, sessions CaptureSessionStore, rules MockRuleStore, pairingTokens PairingTokenStore, shares ShareStore, cfg CaptureConfig) *CaptureManager {
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
	if cfg.MaxSessionTrafficEntries <= 0 {
		cfg.MaxSessionTrafficEntries = DefaultCaptureConfig().MaxSessionTrafficEntries
	}
	if cfg.TrafficRetention <= 0 {
		cfg.TrafficRetention = DefaultCaptureConfig().TrafficRetention
	}
	return &CaptureManager{
		devices:       devices,
		sessions:      sessions,
		rules:         rules,
		pairingTokens: pairingTokens,
		sharesStore:   shares,
		cfg:           cfg,
		log:           slog.Default(),
		viewers:       make(map[string]map[string]capture.ViewerLease),
		traffic:       make(map[string][]*capture.TrafficEntry),
		retained:      make(map[string]*retainedSession),
		trafficIndex:  make(map[string]*trafficIndexEntry),
		stopCh:        make(chan struct{}),
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
// QR pairing tokens (M9, contract v0.8.0)
// ============================================================================

// pairingTokenTTL is how long a QR pairing token stays valid (10 minutes,
// D5): one QR can onboard several devices within the window.
const pairingTokenTTL = 10 * time.Minute

// CreatePairingToken issues a new pairing token for the given user and app.
// The same (user, app) may be issued repeatedly; earlier tokens remain valid
// until their TTL (reusable, D5 — validation never consumes a token).
func (m *CaptureManager) CreatePairingToken(ctx context.Context, user, app string) (*account.PairingToken, error) {
	token, err := account.NewToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := &account.PairingToken{
		Token:     token,
		User:      user,
		App:       app,
		CreatedAt: now,
		ExpiresAt: now.Add(pairingTokenTTL),
	}
	if err := m.pairingTokens.Create(ctx, p); err != nil {
		return nil, err
	}
	out := *p
	return &out, nil
}

// ValidatePairingToken resolves a pairing token and checks it is still valid
// and bound to the given app. Unknown, expired, or app-mismatched tokens map
// to ErrPairingTokenInvalid (handler surfaces 403 pairing_token_invalid, so
// the SDK can tell the user the QR code has expired).
func (m *CaptureManager) ValidatePairingToken(ctx context.Context, token, app string) (*account.PairingToken, error) {
	p, err := m.pairingTokens.GetByToken(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrPairingTokenInvalid
		}
		return nil, err
	}
	if !p.Valid(time.Now()) || p.App != app {
		return nil, ErrPairingTokenInvalid
	}
	out := *p
	return &out, nil
}

// PurgeExpiredPairingTokens deletes every pairing token expired before now
// (hourly janitor; expired tokens are also rejected at validation time, so
// this is housekeeping only).
func (m *CaptureManager) PurgeExpiredPairingTokens(ctx context.Context) {
	if _, err := m.pairingTokens.DeleteExpired(ctx, time.Now()); err != nil {
		m.log.Warn("capture health check: purge expired pairing tokens failed", "error", err)
	}
}

// GetPairingToken returns a token by value without validating expiry/app
// (used by the Web status poll: expired tokens must still report their
// paired devices so the UI can finish the naming flow).
func (m *CaptureManager) GetPairingToken(ctx context.Context, token string) (*account.PairingToken, error) {
	return m.pairingTokens.GetByToken(ctx, token)
}

// RecordPairingUse appends a did to a token's paired-device list so the Web
// can detect that the QR was scanned (M9.3-fix). The register handler calls
// this after a successful pairing registration; failures are logged and do
// not fail the registration (status tracking is best-effort).
func (m *CaptureManager) RecordPairingUse(ctx context.Context, token, did string) {
	if err := m.pairingTokens.RecordPairingUse(ctx, token, did, time.Now()); err != nil {
		m.log.Warn("capture: record pairing use failed", "error", err, "token_prefix", token[:min(8, len(token))])
	}
}
