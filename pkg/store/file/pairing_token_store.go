package file

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// pairingTokenStore implements store.PairingTokenStore using the FileStore's
// in-memory data + debounced persistence (same pattern as authSessionStore).
type pairingTokenStore struct {
	fs *FileStore
}

// Create adds a new pairing token. Token must be unique.
func (s *pairingTokenStore) Create(ctx context.Context, p *account.PairingToken) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.PairingTokens {
		if existing.Token == p.Token {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.PairingTokens = append(s.fs.data.PairingTokens, p)
	s.fs.markDirty()
	return nil
}

// GetByToken returns a single pairing token by token.
func (s *pairingTokenStore) GetByToken(ctx context.Context, token string) (*account.PairingToken, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, p := range s.fs.data.PairingTokens {
		if p.Token == token {
			out := *p
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

// RecordPairingUse appends a did to the token's paired-device list. Repeated
// dids are deduplicated (a device registering again — heartbeat re-register —
// must not create duplicate entries).
func (s *pairingTokenStore) RecordPairingUse(ctx context.Context, token, did string, at time.Time) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, p := range s.fs.data.PairingTokens {
		if p.Token != token {
			continue
		}
		for _, u := range p.PairedDevices {
			if u.Did == did {
				return nil // already recorded
			}
		}
		s.fs.data.PairingTokens[i].PairedDevices = append(p.PairedDevices, account.PairingUse{Did: did, RegisteredAt: at})
		s.fs.markDirty()
		return nil
	}
	return store.ErrNotFound
}

// DeleteExpired removes every token expired before now and returns the number
// of deleted tokens.
func (s *pairingTokenStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	kept := s.fs.data.PairingTokens[:0]
	deleted := 0
	for _, p := range s.fs.data.PairingTokens {
		if !p.Valid(now) {
			deleted++
			continue
		}
		kept = append(kept, p)
	}
	s.fs.data.PairingTokens = kept
	if deleted > 0 {
		s.fs.markDirty()
	}
	return deleted, nil
}
