package file

import (
	"context"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// mockRuleStore implements store.MockRuleStore using the FileStore's in-memory
// data + debounced persistence (same pattern as deviceStore / captureSessionStore).
type mockRuleStore struct {
	fs *FileStore
}

// versionKey joins (app, did) into the key used for the per-device rule version.
func versionKey(app, did string) string {
	return app + "\x00" + did
}

// List returns all mock rules matching the filter (device-scoped; "" dimension
// = no filter), preserving insertion order.
func (s *mockRuleStore) List(ctx context.Context, filter *store.MockRuleFilter) ([]*capture.MockRule, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	result := make([]*capture.MockRule, 0, len(s.fs.data.MockRules))
	for _, r := range s.fs.data.MockRules {
		if filter != nil {
			if filter.App != "" && r.App != filter.App {
				continue
			}
			if filter.Did != "" && r.Did != filter.Did {
				continue
			}
		}
		// Return a copy: rule janitors mutate LastUsedAt/Enabled on the result
		// before calling Update; a live pointer would race with concurrent reads.
		c := *r
		result = append(result, &c)
	}
	return result, nil
}

// Get returns a rule by ID.
func (s *mockRuleStore) Get(ctx context.Context, id string) (*capture.MockRule, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, r := range s.fs.data.MockRules {
		if r.ID == id {
			c := *r
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

// Create adds a new rule. The ID must be unique.
func (s *mockRuleStore) Create(ctx context.Context, r *capture.MockRule) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.MockRules {
		if existing.ID == r.ID {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.MockRules = append(s.fs.data.MockRules, r)
	s.fs.markDirty()
	return nil
}

// Update replaces an existing rule.
func (s *mockRuleStore) Update(ctx context.Context, r *capture.MockRule) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, existing := range s.fs.data.MockRules {
		if existing.ID == r.ID {
			s.fs.data.MockRules[i] = r
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Delete removes a rule by ID.
func (s *mockRuleStore) Delete(ctx context.Context, id string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, r := range s.fs.data.MockRules {
		if r.ID == id {
			s.fs.data.MockRules = append(s.fs.data.MockRules[:i], s.fs.data.MockRules[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Mutate applies fn to the device's rule set and bumps the (app, did)
// rule-set version under one store lock (4.9), so the rule set and its
// version can never be observed half-updated. fn receives a fresh slice of
// the device's current rules (safe to append/reorder/remove) and returns the
// new set; any error aborts with the store untouched.
func (s *mockRuleStore) Mutate(ctx context.Context, app, did string, fn func([]*capture.MockRule) ([]*capture.MockRule, bool, error)) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	all := s.fs.data.MockRules
	deviceRules := make([]*capture.MockRule, 0, len(all))
	for _, r := range all {
		if r.App == app && r.Did == did {
			deviceRules = append(deviceRules, r)
		}
	}
	updated, changed, err := fn(deviceRules)
	if err != nil {
		return 0, err
	}
	if !changed {
		return s.fs.data.RuleVersions[versionKey(app, did)], nil
	}

	// Replace this device's rules in place, preserving every other device's rows.
	out := make([]*capture.MockRule, 0, len(all)-len(deviceRules)+len(updated))
	for _, r := range all {
		if r.App == app && r.Did == did {
			continue
		}
		out = append(out, r)
	}
	out = append(out, updated...)
	s.fs.data.MockRules = out

	k := versionKey(app, did)
	v := s.fs.data.RuleVersions[k] + 1
	if s.fs.data.RuleVersions == nil {
		s.fs.data.RuleVersions = make(map[string]int)
	}
	s.fs.data.RuleVersions[k] = v
	s.fs.markDirty()
	return v, nil
}

// GetRuleVersion returns the current rule-set version for (app, did) (0 when
// no rules have ever been written).
func (s *mockRuleStore) GetRuleVersion(ctx context.Context, app, did string) (int, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	return s.fs.data.RuleVersions[versionKey(app, did)], nil
}

// BumpRuleVersion increments and returns the new rule-set version for (app, did).
func (s *mockRuleStore) BumpRuleVersion(ctx context.Context, app, did string) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	k := versionKey(app, did)
	v := s.fs.data.RuleVersions[k] + 1
	if s.fs.data.RuleVersions == nil {
		s.fs.data.RuleVersions = make(map[string]int)
	}
	s.fs.data.RuleVersions[k] = v
	s.fs.markDirty()
	return v, nil
}
