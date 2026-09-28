package store

import (
	"context"
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
// same Method+Path it returns ErrRuleConflict (409). The conflict check, the
// insert and the rule-set version bump run in ONE store lock (4.9) — no reader
// can observe the rule without its version. caller stamps the rule's Owner
// (O4.1: creator username, server-side field); nil caller (--no-auth) leaves
// Owner empty, which M3-2 treats as a legacy rule (admin-manageable only).
// Every developer may create rules; ownership is established by this stamp.
func (m *CaptureManager) CreateMockRule(ctx context.Context, app, did string, in *capture.MockRuleInput, caller *RuleCaller) (*capture.MockRuleView, int, error) {
	now := time.Now()
	var created *capture.MockRule
	version, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		if in.Enabled {
			for _, r := range rules {
				if r.Enabled && r.Method == in.Method && r.Path == in.Path {
					return nil, false, ErrRuleConflict
				}
			}
		}
		var owner string
		if caller != nil {
			owner = caller.Username
		}
		created = &capture.MockRule{
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
			Owner:      owner,
			UpdatedBy:  owner,
		}
		return append(rules, created), true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	views, _, _, err := m.evaluateRules(ctx, app, did)
	if err != nil {
		return nil, version, err
	}
	for _, v := range views {
		if v.ID == created.ID {
			return v, version, nil
		}
	}
	return &capture.MockRuleView{MockRule: created, Effective: in.Enabled}, version, nil
}

// canManageRule reports whether caller may mutate rule r (O4.2/O4.3). nil
// caller (--no-auth smoke mode) always passes, mirroring the requireAuth
// bypass; admins pass; otherwise only the rule's owner passes. A rule with an
// empty owner is a legacy rule (pre-O4) — admin-manageable only (O4.3).
func canManageRule(caller *RuleCaller, r *capture.MockRule) bool {
	if caller == nil {
		return true
	}
	if caller.IsAdmin {
		return true
	}
	return r.Owner != "" && r.Owner == caller.Username
}

// UpdateMockRule edits a rule's canned response, note, and/or enabled switch
// (M5). The match key (Method+Path) and the source snapshot are immutable —
// the input type UpdateMockRuleInput deliberately omits them. Turning the switch
// on is rejected with ErrRuleConflict if another enabled rule already matches
// the rule's (frozen) interface. An absent Enabled pointer leaves the current
// switch untouched. The update and the version bump run in ONE store lock (4.9).
// O4.2: only the rule's owner or an admin may edit/toggle; any other caller
// gets ErrRuleForbidden. For non-admin callers a missing ruleID also returns
// ErrRuleForbidden (not ErrRuleNotFound) so rule existence cannot be probed.
func (m *CaptureManager) UpdateMockRule(ctx context.Context, app, did, ruleID string, in *capture.UpdateMockRuleInput, caller *RuleCaller) (*capture.MockRuleView, int, error) {
	var updated *capture.MockRule
	version, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		var existing *capture.MockRule
		for _, r := range rules {
			if r.ID == ruleID {
				existing = r
				break
			}
		}
		if existing == nil {
			// 不泄露规则存在性：非 admin 对未知 ruleID 统一按无权限处理。
			if caller != nil && !caller.IsAdmin {
				return nil, false, ErrRuleForbidden
			}
			return nil, false, ErrRuleNotFound
		}
		// O4.2 权限矩阵：编辑/启停仅 owner 与 admin。
		if !canManageRule(caller, existing) {
			return nil, false, ErrRuleForbidden
		}

		// M7: note is required only when the PUT actually edits the canned
		// response (statusCode/headers/body changed). A pure toggle echoes the
		// stored response unchanged and may leave the note blank — rules created
		// from a capture ("Mock 此请求") carry no note and enabling them must not
		// force an edit.
		if !reflect.DeepEqual(existing.Response, in.Response) && strings.TrimSpace(in.Note) == "" {
			return nil, false, ErrNoteRequired
		}

		// Enforce single-active on the frozen interface when the edit turns the
		// rule on (Enabled pointer present and true, while currently off).
		if in.Enabled != nil && *in.Enabled && !existing.Enabled {
			for _, r := range rules {
				if r.ID != ruleID && r.Enabled && r.Method == existing.Method && r.Path == existing.Path {
					return nil, false, ErrRuleConflict
				}
			}
		}

		c := *existing
		c.Response = in.Response
		c.Note = in.Note
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		c.UpdatedAt = time.Now()
		// Editing a rule or toggling it counts as "used" (M4 sliding window).
		c.LastUsedAt = c.UpdatedAt
		// O4.1: stamp the last modifier from the current session user.
		if caller != nil {
			c.UpdatedBy = caller.Username
		}
		updated = &c

		out := make([]*capture.MockRule, 0, len(rules))
		for _, r := range rules {
			if r.ID == ruleID {
				out = append(out, updated)
			} else {
				out = append(out, r)
			}
		}
		return out, true, nil
	})
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

// DeleteMockRule removes a rule by ID (scoped to app/did). The delete and the
// version bump run in ONE store lock (4.9) so the SDK always sees the rule-set
// version advance together with the removal. O4.2: only the rule's owner or an
// admin may delete; other callers get ErrRuleForbidden (and, like Update, a
// missing ruleID is masked as ErrRuleForbidden for non-admin callers).
func (m *CaptureManager) DeleteMockRule(ctx context.Context, app, did, ruleID string, caller *RuleCaller) (int, error) {
	return m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		for i, r := range rules {
			if r.ID == ruleID {
				if !canManageRule(caller, r) {
					return nil, false, ErrRuleForbidden
				}
				out := make([]*capture.MockRule, 0, len(rules)-1)
				out = append(out, rules[:i]...)
				out = append(out, rules[i+1:]...)
				return out, true, nil
			}
		}
		if caller != nil && !caller.IsAdmin {
			return nil, false, ErrRuleForbidden
		}
		return nil, false, ErrRuleNotFound
	})
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
// capture session the user must re-enable each one manually. The whole disable
// set and the version bump run in ONE store lock (4.9) so the SDK drops them
// atomically.
func (m *CaptureManager) disableDeviceRules(ctx context.Context, app, did string) {
	if _, err := m.rules.Mutate(ctx, app, did, func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		changed := false
		for i, r := range rules {
			if !r.Enabled {
				continue
			}
			c := *r
			c.Enabled = false
			rules[i] = &c
			changed = true
		}
		if !changed {
			return rules, false, nil
		}
		return rules, true, nil
	}); err != nil {
		m.log.Warn("session end: disable rules failed", "app", app, "did", did, "error", err)
	}
}
