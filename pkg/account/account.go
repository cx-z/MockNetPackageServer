// Package account provides the MockNetPack account model and the password /
// token primitives for the  account system (契约 v0.6.0).
//
// The model is platform-neutral (G5) and has no dependency on the store or
// HTTP layers: persistence lives in pkg/store, HTTP semantics in pkg/admin.
//
// Security notes:
//   - Passwords are never stored in plaintext; HashPassword produces a
//     PBKDF2-HMAC-SHA256 encoding with a random per-user salt (stdlib
//     crypto/pbkdf2, OWASP-recommended iteration count).
//   - Session tokens are 32 random bytes hex-encoded; the server persists
//     them and revokes them on logout/expiry (服务端吊销,  拍板).
package account

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Role is the account role. Only two roles exist :
// admin sees every device; dev sees only devices it owns.
type Role string

const (
	// RoleAdmin is the administrator role. Admin accounts are NOT created via
	// the open registration API — the only creation path is the CLI
	// `mockd start --create-admin` ( 拍板).
	RoleAdmin Role = "admin"
	// RoleDev is the developer role. Open registration only produces this role.
	RoleDev Role = "dev"
)

// Valid reports whether the role is one of the two supported roles.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleDev
}

// User is a MockNetPack account. PasswordHash is the PBKDF2 encoding produced
// by HashPassword and must never be returned to clients.
type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
}

// AuthSession is a server-issued session token : created on login,
// persisted server-side, revoked on logout (server-side deletion) or expiry.
type AuthSession struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Valid reports whether the session is still usable at the given time.
func (s *AuthSession) Valid(now time.Time) bool {
	return now.Before(s.ExpiresAt)
}

// PBKDF2 parameters for password hashing (stdlib crypto/pbkdf2, Go 1.24+).
const (
	pbkdf2Iterations = 210_000 // OWASP-recommended minimum for PBKDF2-HMAC-SHA256
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
	pbkdf2Prefix     = "pbkdf2-sha256"
)

// HashPassword encodes password as "pbkdf2-sha256$<iter>$<salt-hex>$<key-hex>".
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("empty password")
	}
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		pbkdf2Prefix, pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// VerifyPassword checks password against an encoded hash in constant time.
// Malformed hashes always return false.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Prefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken generates a session token: 32 random bytes hex-encoded (64 chars).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// dummyPasswordHash is a valid PBKDF2 hash used as a constant-time decoy for
// unknown users at login (4.16): VerifyPassword against it costs exactly the
// same as a real user's hash, so login latency cannot reveal whether a
// username exists. Computed once per process; the decoy value is irrelevant,
// only its verification cost matters.
var dummyPasswordHash = func() string {
	h, err := HashPassword("mockd-login-decoy")
	if err != nil {
		panic(fmt.Sprintf("account: dummy hash: %v", err))
	}
	return h
}()

// DummyPasswordHash returns the process-wide decoy hash for constant-time
// login on unknown usernames (4.16).
func DummyPasswordHash() string { return dummyPasswordHash }
