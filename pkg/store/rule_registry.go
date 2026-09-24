package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/pkg/capture"
)

// File: rule_registry.go
// Mock rules: CRUD, Effective/conflict evaluation, version, retention janitor,
// disable-on-session-end (pure move from capture_registry.go).
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
