package file

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/store"
)

// shareStore implements store.ShareStore using the FileStore's in-memory data
// + debounced persistence (same pattern as pairingTokenStore).
type shareStore struct {
	fs *FileStore
}

// List returns all persisted share snapshots.
func (s *shareStore) List(ctx context.Context) ([]*store.ShareSnapshot, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	out := make([]*store.ShareSnapshot, 0, len(s.fs.data.Shares))
	for _, snap := range s.fs.data.Shares {
		// Return a copy: callers may mutate the snapshot; a live pointer
		// would race with concurrent readers (same pattern as deviceStore).
		c := *snap
		out = append(out, &c)
	}
	return out, nil
}

// Create adds a new share snapshot. The ID must be unique.
func (s *shareStore) Create(ctx context.Context, snap *store.ShareSnapshot) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.Shares {
		if existing.ShareID == snap.ShareID {
			return store.ErrAlreadyExists
		}
	}
	s.fs.data.Shares = append(s.fs.data.Shares, snap)
	s.fs.markDirty()
	return nil
}

// Delete removes a share snapshot by ID.
func (s *shareStore) Delete(ctx context.Context, id string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, snap := range s.fs.data.Shares {
		if snap.ShareID == id {
			s.fs.data.Shares = append(s.fs.data.Shares[:i], s.fs.data.Shares[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// DeleteExpired removes every share snapshot expired before now and returns
// the number of deleted shares (hourly janitor).
func (s *shareStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	kept := s.fs.data.Shares[:0]
	deleted := 0
	for _, snap := range s.fs.data.Shares {
		if now.After(snap.ExpiresAt) {
			deleted++
			continue
		}
		kept = append(kept, snap)
	}
	if deleted > 0 {
		s.fs.data.Shares = kept
		s.fs.markDirty()
	}
	return deleted, nil
}
