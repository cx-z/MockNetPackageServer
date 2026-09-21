package file

import (
	"context"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

func newAccountTestStore(t *testing.T) *FileStore {
	t.Helper()
	cfg := store.DefaultConfig()
	cfg.DataDir = t.TempDir()
	fs := New(cfg)
	if err := fs.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

func TestUserStoreCRUD(t *testing.T) {
	fs := newAccountTestStore(t)
	us := fs.Users()

	u := &account.User{Username: "alice", PasswordHash: "hash", Role: account.RoleDev, CreatedAt: time.Now()}
	if err := us.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Duplicate create is rejected.
	if err := us.Create(context.Background(), &account.User{Username: "alice", Role: account.RoleDev}); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}

	got, err := us.GetByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.Username != "alice" || got.Role != account.RoleDev || got.PasswordHash != "hash" {
		t.Fatalf("GetByUsername = %+v", got)
	}

	// Get missing user.
	if _, err := us.GetByUsername(context.Background(), "nobody"); err != store.ErrNotFound {
		t.Fatalf("GetByUsername(missing) = %v, want ErrNotFound", err)
	}

	// Update.
	got.Role = account.RoleAdmin
	if err := us.Update(context.Background(), got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, _ := us.GetByUsername(context.Background(), "alice")
	if again.Role != account.RoleAdmin {
		t.Fatalf("after update role = %q, want admin", again.Role)
	}

	// List.
	_ = us.Create(context.Background(), &account.User{Username: "bob", Role: account.RoleDev})
	all, err := us.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List len = %d, want 2", len(all))
	}

	// Delete.
	if err := us.Delete(context.Background(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := us.GetByUsername(context.Background(), "alice"); err != store.ErrNotFound {
		t.Fatalf("GetByUsername after Delete = %v, want ErrNotFound", err)
	}
}

func TestUserStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	cfg := store.DefaultConfig()
	cfg.DataDir = dir

	fs := New(cfg)
	if err := fs.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := fs.Users().Create(context.Background(), &account.User{Username: "persist", PasswordHash: "h", Role: account.RoleDev}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fs2 := New(cfg)
	if err := fs2.Open(context.Background()); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	got, err := fs2.Users().GetByUsername(context.Background(), "persist")
	if err != nil {
		t.Fatalf("GetByUsername after reopen: %v", err)
	}
	if got.PasswordHash != "h" || got.Role != account.RoleDev {
		t.Fatalf("reopened user = %+v", got)
	}
}

func TestAuthSessionStore(t *testing.T) {
	fs := newAccountTestStore(t)
	ss := fs.AuthSessions()

	now := time.Now()
	valid := &account.AuthSession{Token: "tok-valid", Username: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	expired := &account.AuthSession{Token: "tok-expired", Username: "alice", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	for _, s := range []*account.AuthSession{valid, expired} {
		if err := ss.Create(context.Background(), s); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Duplicate token rejected.
	if err := ss.Create(context.Background(), valid); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}

	got, err := ss.GetByToken(context.Background(), "tok-valid")
	if err != nil {
		t.Fatalf("GetByToken: %v", err)
	}
	if got.Username != "alice" || !got.Valid(now) {
		t.Fatalf("GetByToken = %+v", got)
	}
	if _, err := ss.GetByToken(context.Background(), "nope"); err != store.ErrNotFound {
		t.Fatalf("GetByToken(missing) = %v, want ErrNotFound", err)
	}

	// Delete (server-side revocation).
	if err := ss.Delete(context.Background(), "tok-valid"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := ss.GetByToken(context.Background(), "tok-valid"); err != store.ErrNotFound {
		t.Fatalf("GetByToken after Delete = %v, want ErrNotFound", err)
	}

	// DeleteExpired removes only expired sessions.
	if n, err := ss.DeleteExpired(context.Background(), now); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	} else if n != 1 {
		t.Fatalf("DeleteExpired deleted %d, want 1", n)
	}
	if _, err := ss.GetByToken(context.Background(), "tok-expired"); err != store.ErrNotFound {
		t.Fatalf("expired session still present: %v", err)
	}
}
