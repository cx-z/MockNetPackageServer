package file

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// userStore implements store.UserStore using the FileStore's in-memory data +
// debounced persistence (same pattern as deviceStore).
type userStore struct {
	fs *FileStore
}

// GetByUsername returns a single user by username.
func (s *userStore) GetByUsername(ctx context.Context, username string) (*account.User, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, u := range s.fs.data.Users {
		if u.Username == username {
			out := *u
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

// List returns all users.
func (s *userStore) List(ctx context.Context) ([]*account.User, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	out := make([]*account.User, 0, len(s.fs.data.Users))
	for _, u := range s.fs.data.Users {
		c := *u
		out = append(out, &c)
	}
	return out, nil
}

// Create adds a new user. Username must be unique.
func (s *userStore) Create(ctx context.Context, u *account.User) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.Users {
		if existing.Username == u.Username {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.Users = append(s.fs.data.Users, u)
	s.fs.markDirty()
	return nil
}

// Update replaces an existing user.
func (s *userStore) Update(ctx context.Context, u *account.User) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, existing := range s.fs.data.Users {
		if existing.Username == u.Username {
			s.fs.data.Users[i] = u
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Delete removes a user by username.
func (s *userStore) Delete(ctx context.Context, username string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, u := range s.fs.data.Users {
		if u.Username == username {
			s.fs.data.Users = append(s.fs.data.Users[:i], s.fs.data.Users[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// authSessionStore implements store.AuthSessionStore using the FileStore's
// in-memory data + debounced persistence.
type authSessionStore struct {
	fs *FileStore
}

// Create adds a new session token. Token must be unique.
func (s *authSessionStore) Create(ctx context.Context, sess *account.AuthSession) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.AuthSessions {
		if existing.Token == sess.Token {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.AuthSessions = append(s.fs.data.AuthSessions, sess)
	s.fs.markDirty()
	return nil
}

// GetByToken returns a single session by token.
func (s *authSessionStore) GetByToken(ctx context.Context, token string) (*account.AuthSession, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, sess := range s.fs.data.AuthSessions {
		if sess.Token == token {
			out := *sess
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

// Delete revokes a session by token.
func (s *authSessionStore) Delete(ctx context.Context, token string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, sess := range s.fs.data.AuthSessions {
		if sess.Token == token {
			s.fs.data.AuthSessions = append(s.fs.data.AuthSessions[:i], s.fs.data.AuthSessions[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// DeleteExpired removes every session expired before now and returns the
// number of deleted sessions.
func (s *authSessionStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	kept := s.fs.data.AuthSessions[:0]
	deleted := 0
	for _, sess := range s.fs.data.AuthSessions {
		if !sess.Valid(now) {
			deleted++
			continue
		}
		kept = append(kept, sess)
	}
	s.fs.data.AuthSessions = kept
	if deleted > 0 {
		s.fs.markDirty()
	}
	return deleted, nil
}
