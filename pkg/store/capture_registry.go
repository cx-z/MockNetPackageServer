package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

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
		HeartbeatInterval:  20 * time.Second,
		HeartbeatTimeout:   60 * time.Second,
		ViewerTTL:          120 * time.Second,
		MockRuleRetention:  7 * 24 * time.Hour,
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
