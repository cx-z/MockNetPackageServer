// Package account provides MockNetPack account models and long-lived API keys.
//
// Purpose: give scripts / Agent tooling a machine-native auth channel that
// outlives the 7-day login session token  — an API key is created once,
// the plaintext is returned exactly once, and the server persists only a
// SHA-256 hash. Keys authenticate through the same `Authorization: Bearer
// <key>` header as session tokens (the middleware tries session first, then
// API key), so every Web-facing /api/v1 route works unchanged for key holders.
//
// Security notes:
//   - Keys are 256-bit random (crypto/rand), prefixed "mnpk_" so the middleware
//     can route them without a store probe and logs can recognize them without
//     printing them.
//   - SHA-256 (not PBKDF2) is the right KDF here: the key has full 256-bit
//     entropy, so there is no brute-force surface that iteration protects, and
//     per-request verification stays cheap (PBKDF2's 210k iterations would
//     penalize every authenticated call for no gain).
//   - ExpiresAt zero means "long-lived" (no expiry) — the  default.
package account

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/getmockd/mockd/internal/id"
)

// APIKeyPrefix marks API keys so the auth middleware can route them without a
// store probe and logs never print the plaintext. Session tokens (32 random
// bytes hex) never carry this prefix.
const APIKeyPrefix = "mnpk_"

// APIKey is a long-lived machine credential bound to one account. KeyHash is
// the SHA-256 hex of the plaintext — the plaintext exists only at creation
// time (returned once) and is never stored.
type APIKey struct {
	// ID is the stable identifier used for listing and revocation.
	ID string `json:"id"`
	// KeyHash is the SHA-256 hex of the plaintext key (never returned).
	KeyHash string `json:"keyHash"`
	// KeyPrefix is the first "mnpk_" + 10 chars of the plaintext — enough for
	// humans/logs to recognize the key without revealing it.
	KeyPrefix string `json:"keyPrefix"`
	// Username is the owning account.
	Username string `json:"username"`
	// CreatedAt is when the key was issued.
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt is when the key stops working. Zero = long-lived ( default).
	ExpiresAt time.Time `json:"expiresAt"`
}

// Valid reports whether the key is still usable at the given time. Zero
// ExpiresAt means "long-lived" (never expires).
func (k *APIKey) Valid(now time.Time) bool {
	return k.ExpiresAt.IsZero() || now.Before(k.ExpiresAt)
}

// HashAPIKey returns the canonical SHA-256 hex digest of a plaintext key.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// IsAPIKey reports whether a Bearer token looks like a long-lived API key
// (prefixed) rather than a session token.
func IsAPIKey(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix)
}

// VerifyAPIKey compares a plaintext key against a stored hash in constant
// time. A malformed hash always fails.
func VerifyAPIKey(hash, key string) bool {
	want, err := hex.DecodeString(hash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// NewAPIKey creates a long-lived API key for the given account. It returns the
// stored model (hash only) and the plaintext — the plaintext is returned
// exactly once (at creation) and must never be persisted or logged.
func NewAPIKey(username string, now time.Time) (*APIKey, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	plain := APIKeyPrefix + hex.EncodeToString(raw)

	k := &APIKey{
		ID:        id.UUID(),
		KeyHash:   HashAPIKey(plain),
		KeyPrefix: APIKeyPrefix + plain[len(APIKeyPrefix):len(APIKeyPrefix)+10],
		Username:  username,
		CreatedAt: now,
	}
	return k, plain, nil
}
