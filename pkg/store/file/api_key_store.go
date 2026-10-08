package file

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// apiKeyStore implements store.APIKeyStore using the FileStore's in-memory
// data + debounced persistence (same pattern as authSessionStore, M7.1).
//
// Only the SHA-256 hash (KeyHash) is persisted; the plaintext key exists at
// creation time only and is never written to disk or logged.
type apiKeyStore struct {
	fs *FileStore
}

// Create adds a new API key. ID must be unique.
func (s *apiKeyStore) Create(ctx context.Context, k *account.APIKey) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}
	for _, existing := range s.fs.data.APIKeys {
		if existing.ID == k.ID {
			return store.ErrAlreadyExists
		}
	}
	s.fs.data.APIKeys = append(s.fs.data.APIKeys, k)
	s.fs.markDirty()
	return nil
}

// GetByID returns a single key by its stable ID.
func (s *apiKeyStore) GetByID(ctx context.Context, id string) (*account.APIKey, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	for _, k := range s.fs.data.APIKeys {
		if k.ID == id {
			out := *k
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

// GetByHash resolves a key by its SHA-256 hash (auth lookup).
func (s *apiKeyStore) GetByHash(ctx context.Context, hash string) (*account.APIKey, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	for _, k := range s.fs.data.APIKeys {
		if k.KeyHash == hash {
			out := *k
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

// ListByUsername returns every key of one account (creation order).
func (s *apiKeyStore) ListByUsername(ctx context.Context, username string) ([]*account.APIKey, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	out := make([]*account.APIKey, 0)
	for _, k := range s.fs.data.APIKeys {
		if k.Username == username {
			c := *k
			out = append(out, &c)
		}
	}
	return out, nil
}

// Delete revokes a key by ID.
func (s *apiKeyStore) Delete(ctx context.Context, id string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}
	for i, k := range s.fs.data.APIKeys {
		if k.ID == id {
			s.fs.data.APIKeys = append(s.fs.data.APIKeys[:i], s.fs.data.APIKeys[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// DeleteExpired removes every expired key and returns the number deleted.
// Housekeeping only — expired keys are also rejected lazily at auth time.
func (s *apiKeyStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}
	kept := s.fs.data.APIKeys[:0]
	deleted := 0
	for _, k := range s.fs.data.APIKeys {
		if !k.Valid(now) {
			deleted++
			continue
		}
		kept = append(kept, k)
	}
	s.fs.data.APIKeys = kept
	if deleted > 0 {
		s.fs.markDirty()
	}
	return deleted, nil
}
