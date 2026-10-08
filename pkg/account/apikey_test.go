package account

import (
	"strings"
	"testing"
	"time"
)

func TestNewAPIKey_ShapeAndUniqueness(t *testing.T) {
	now := time.Now()
	k1, plain1, err := NewAPIKey("alice", now)
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	k2, plain2, err := NewAPIKey("alice", now)
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}

	// Plaintext carries the prefix, is long enough, and differs per call.
	if !strings.HasPrefix(plain1, APIKeyPrefix) || !strings.HasPrefix(plain2, APIKeyPrefix) {
		t.Fatalf("plaintext lacks prefix: %q / %q", plain1, plain2)
	}
	if len(plain1) != len(APIKeyPrefix)+64 {
		t.Fatalf("plaintext length = %d, want %d", len(plain1), len(APIKeyPrefix)+64)
	}
	if plain1 == plain2 {
		t.Fatal("two keys must differ")
	}
	if k1.ID == k2.ID {
		t.Fatal("key IDs must differ")
	}

	// The model never carries the plaintext.
	if strings.Contains(k1.KeyHash, plain1) || k1.KeyPrefix == plain1 {
		t.Fatal("model leaks plaintext")
	}
	if !strings.HasPrefix(k1.KeyPrefix, APIKeyPrefix) {
		t.Fatalf("keyPrefix must start with %q, got %q", APIKeyPrefix, k1.KeyPrefix)
	}
}

func TestVerifyAPIKey(t *testing.T) {
	_, plain, err := NewAPIKey("alice", time.Now())
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	hash := HashAPIKey(plain)

	if !VerifyAPIKey(hash, plain) {
		t.Fatal("correct key must verify")
	}
	if VerifyAPIKey(hash, plain+"x") {
		t.Fatal("tampered key must not verify")
	}
	if VerifyAPIKey("not-hex", plain) {
		t.Fatal("malformed hash must not verify")
	}
	if VerifyAPIKey(strings.Repeat("0", 64), plain) {
		t.Fatal("wrong hash must not verify")
	}
}

func TestAPIKey_Valid(t *testing.T) {
	now := time.Now()
	k := &APIKey{ExpiresAt: time.Time{}} // zero = long-lived
	if !k.Valid(now) {
		t.Fatal("zero ExpiresAt must mean long-lived")
	}
	k.ExpiresAt = now.Add(-time.Hour)
	if k.Valid(now) {
		t.Fatal("expired key must be invalid")
	}
	k.ExpiresAt = now.Add(time.Hour)
	if !k.Valid(now) {
		t.Fatal("future ExpiresAt must be valid")
	}
}

func TestIsAPIKey(t *testing.T) {
	if !IsAPIKey(APIKeyPrefix + "abcdef") {
		t.Fatal("prefixed token must be recognized as API key")
	}
	if IsAPIKey("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("session token (no prefix) must not be recognized as API key")
	}
}
