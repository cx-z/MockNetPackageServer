package file

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// pushMockRuleStore implements store.PushMockRuleStore using the FileStore's
// in-memory data + debounced persistence (独立规则集与独立版本号
// key = app + "\x00" + did，与 HTTP mockRuleStore 互不干扰)。
type pushMockRuleStore struct {
	fs *FileStore
}

// List returns all push mock rules matching the filter (device-scoped; "" dimension
// = no filter), preserving insertion order.
func (s *pushMockRuleStore) List(ctx context.Context, filter *store.MockRuleFilter) ([]*capture.PushMockRule, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	result := make([]*capture.PushMockRule, 0, len(s.fs.data.PushMockRules))
	for _, r := range s.fs.data.PushMockRules {
		if filter != nil {
			if filter.App != "" && r.App != filter.App {
				continue
			}
			if filter.Did != "" && r.Did != filter.Did {
				continue
			}
		}
		// Return a copy: the manager mutates HitCount/LastUsedAt/Enabled on the
		// result before calling Update; a live pointer would race with reads.
		c := *r
		result = append(result, &c)
	}
	return result, nil
}

// Get returns a push rule by ID.
func (s *pushMockRuleStore) Get(ctx context.Context, id string) (*capture.PushMockRule, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, r := range s.fs.data.PushMockRules {
		if r.ID == id {
			c := *r
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

// Create adds a new push rule. The ID must be unique.
func (s *pushMockRuleStore) Create(ctx context.Context, r *capture.PushMockRule) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.PushMockRules {
		if existing.ID == r.ID {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.PushMockRules = append(s.fs.data.PushMockRules, r)
	s.fs.markDirty()
	return nil
}

// Update replaces an existing push rule.
func (s *pushMockRuleStore) Update(ctx context.Context, r *capture.PushMockRule) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, existing := range s.fs.data.PushMockRules {
		if existing.ID == r.ID {
			s.fs.data.PushMockRules[i] = r
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Delete removes a push rule by ID.
func (s *pushMockRuleStore) Delete(ctx context.Context, id string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, r := range s.fs.data.PushMockRules {
		if r.ID == id {
			s.fs.data.PushMockRules = append(s.fs.data.PushMockRules[:i], s.fs.data.PushMockRules[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Mutate applies fn to the device's push rule set and bumps the (app, did)
// push rule-set version under one store lock, so the push rule set and its
// version can never be observed half-updated (对齐 mockRuleStore.Mutate，但使用
// 独立的 PushRuleVersions 计数器)。
func (s *pushMockRuleStore) Mutate(ctx context.Context, app, did string, fn func([]*capture.PushMockRule) ([]*capture.PushMockRule, bool, error)) (int, error) {
	return mutateDeviceRules(s.fs, &s.fs.data.PushMockRules, &s.fs.data.PushRuleVersions, app, did, fn,
		func(r *capture.PushMockRule) string { return r.App },
		func(r *capture.PushMockRule) string { return r.Did })
}

// GetRuleVersion returns the current push rule-set version for (app, did)
// (0 when no push rules have ever been written).
func (s *pushMockRuleStore) GetRuleVersion(ctx context.Context, app, did string) (int, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	return s.fs.data.PushRuleVersions[versionKey(app, did)], nil
}

// BumpRuleVersion increments and returns the new push rule-set version for (app, did).
func (s *pushMockRuleStore) BumpRuleVersion(ctx context.Context, app, did string) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	k := versionKey(app, did)
	v := s.fs.data.PushRuleVersions[k] + 1
	if s.fs.data.PushRuleVersions == nil {
		s.fs.data.PushRuleVersions = make(map[string]int)
	}
	s.fs.data.PushRuleVersions[k] = v
	s.fs.markDirty()
	return v, nil
}

// pushRuleCommandStore implements store.PushRuleCommandStore (模拟推送
// 形态：pending 指令队列，独立于 push 规则集/版本号)。
type pushRuleCommandStore struct {
	fs *FileStore
}

func (s *pushRuleCommandStore) Append(ctx context.Context, app, did string, cmd *capture.PushRuleCommand) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}
	if s.fs.data.PushRuleCommands == nil {
		s.fs.data.PushRuleCommands = make(map[string][]*capture.PushRuleCommand)
	}
	k := versionKey(app, did)
	// 队列上限防积压：设备离线时指令可能持续堆积，超上限丢弃最旧。
	const maxPending = 100
	q := s.fs.data.PushRuleCommands[k]
	if len(q) >= maxPending {
		q = q[len(q)-maxPending+1:]
	}
	s.fs.data.PushRuleCommands[k] = append(q, cmd)
	s.fs.markDirty()
	return nil
}

func (s *pushRuleCommandStore) ListPending(ctx context.Context, app, did string) ([]*capture.PushRuleCommand, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	q := s.fs.data.PushRuleCommands[versionKey(app, did)]
	out := make([]*capture.PushRuleCommand, 0, len(q))
	for _, c := range q {
		if !c.Consumed {
			copy := *c
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (s *pushRuleCommandStore) MarkConsumed(ctx context.Context, app, did string, ids []string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]bool, len(ids))
	for _, id := range ids {
		byID[id] = true
	}
	k := versionKey(app, did)
	q := s.fs.data.PushRuleCommands[k]
	changed := false
	for _, c := range q {
		if byID[c.ID] && !c.Consumed {
			c.Consumed = true
			c.ConsumedAt = time.Now()
			changed = true
		}
	}
	if changed {
		s.fs.markDirty()
	}
	return nil
}

func (s *pushRuleCommandStore) PurgeExpired(ctx context.Context, retention time.Duration) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-retention)
	total := 0
	for k, q := range s.fs.data.PushRuleCommands {
		kept := q[:0]
		for _, c := range q {
			if c.CreatedAt.Before(cutoff) {
				total++
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == 0 {
			delete(s.fs.data.PushRuleCommands, k)
		} else {
			s.fs.data.PushRuleCommands[k] = kept
		}
	}
	if total > 0 {
		s.fs.markDirty()
	}
	return total, nil
}
