package account

import (
	"strings"
	"testing"
	"time"
)

var (
	past   = time.Now().Add(-time.Hour)
	future = time.Now().Add(time.Hour)
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("hash format = %q, want pbkdf2-sha256$ prefix", hash)
	}
	if !VerifyPassword(hash, "s3cret-pass") {
		t.Fatal("VerifyPassword: correct password rejected")
	}
	if VerifyPassword(hash, "wrong-pass") {
		t.Fatal("VerifyPassword: wrong password accepted")
	}
	if VerifyPassword(hash, "") {
		t.Fatal("VerifyPassword: empty password accepted")
	}
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	h1, _ := HashPassword("same-pass")
	h2, _ := HashPassword("same-pass")
	if h1 == h2 {
		t.Fatal("two hashes of the same password should differ (random salt)")
	}
}

func TestHashPasswordEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("HashPassword(\"\") should error")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	cases := []string{
		"",
		"plaintext",
		"pbkdf2-sha256$notanumber$aa$bb",
		"pbkdf2-sha256$1000$zz$bb",   // bad salt hex
		"pbkdf2-sha256$1000$aa$zz",   // bad key hex
		"$1$aa$bb",                   // wrong prefix
		"pbkdf2-sha256$1000$aa",      // too few parts
	}
	for _, c := range cases {
		if VerifyPassword(c, "whatever") {
			t.Fatalf("VerifyPassword(%q) should be false", c)
		}
	}
}

func TestNewToken(t *testing.T) {
	t1, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	t2, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(t1) != 64 {
		t.Fatalf("token length = %d, want 64 (32 random bytes hex)", len(t1))
	}
	if t1 == t2 {
		t.Fatal("two tokens should differ")
	}
}

func TestRoleValid(t *testing.T) {
	if !RoleAdmin.Valid() || !RoleDev.Valid() {
		t.Fatal("admin/dev roles should be valid")
	}
	if Role("boss").Valid() {
		t.Fatal("unknown role should be invalid")
	}
}

func TestAuthSessionValid(t *testing.T) {
	s := &AuthSession{Token: "t", ExpiresAt: future}
	if !s.Valid(past) {
		t.Fatal("session should be valid before expiry")
	}
	if s.Valid(future.Add(1)) {
		t.Fatal("session should be invalid after expiry")
	}
}
