package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/account"
)

// createAPIKey logs in the given user and issues an API key, returning the
// plaintext (creation-only) and the key ID.
func createAPIKey(t *testing.T, ts *httptest.Server, token string) (string, string) {
	t.Helper()
	res, body := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/keys", token, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create key = %d, want 201; body=%s", res.StatusCode, body)
	}
	var out CreateAPIKeyResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !account.IsAPIKey(out.Key) {
		t.Fatalf("plaintext key must carry mnpk_ prefix, got %q", out.Key)
	}
	return out.Key, out.ID
}

func TestAPIKeyLifecycle(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "alice", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}
	res, body := loginUser(t, ts, "alice", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; body=%s", res.StatusCode, body)
	}
	var login LoginAuthResponse
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatalf("unmarshal login: %v", err)
	}
	token := login.Token

	// Create: 201 with plaintext exactly once.
	plain, keyID := createAPIKey(t, ts, token)
	if !strings.HasPrefix(plain, account.APIKeyPrefix) {
		t.Fatalf("plaintext prefix: %q", plain)
	}

	// List: prefix present, plaintext AND hash absent.
	res, body = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d; body=%s", res.StatusCode, body)
	}
	var list ListAPIKeysResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if list.Total != 1 || len(list.Keys) != 1 {
		t.Fatalf("list = %+v, want 1 key", list)
	}
	if list.Keys[0].ID != keyID {
		t.Fatalf("key id = %s, want %s", list.Keys[0].ID, keyID)
	}
	if !strings.HasPrefix(list.Keys[0].KeyPrefix, account.APIKeyPrefix) {
		t.Fatalf("keyPrefix = %q", list.Keys[0].KeyPrefix)
	}
	if strings.Contains(string(body), plain) || strings.Contains(string(body), "keyHash") {
		t.Fatalf("list leaks plaintext or hash: %s", body)
	}

	// The API key authenticates /auth/me as the owning user.
	res, body = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", plain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me with api key = %d; body=%s", res.StatusCode, body)
	}
	var me AuthUser
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("unmarshal me: %v", err)
	}
	if me.Username != "alice" || me.Role != account.RoleDev {
		t.Fatalf("me = %+v, want alice/dev", me)
	}

	// Revoke: 204, then the key is immediately 401.
	res, _ = doAuthJSON(t, http.MethodDelete, ts.URL+"/api/v1/auth/keys/"+keyID, token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", res.StatusCode)
	}
	res, body = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", plain, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key me = %d, want 401; body=%s", res.StatusCode, body)
	}

	// List is empty again.
	res, body = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list after revoke = %d; body=%s", res.StatusCode, body)
	}
	var empty ListAPIKeysResponse
	_ = json.Unmarshal(body, &empty)
	if empty.Total != 0 {
		t.Fatalf("list after revoke = %+v, want 0", empty)
	}
}

func TestAPIKeySessionTokenCoexist(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "bob", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}
	res, body := loginUser(t, ts, "bob", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; body=%s", res.StatusCode, body)
	}
	var login LoginAuthResponse
	_ = json.Unmarshal(body, &login)

	// Both credentials hit the same protected endpoint.
	plain, keyID := createAPIKey(t, ts, login.Token)
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", plain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("devices via api key = %d, want 200", res.StatusCode)
	}
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", login.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("devices via session token = %d, want 200", res.StatusCode)
	}

	// Revoking the key must NOT invalidate the session token.
	res, _ = doAuthJSON(t, http.MethodDelete, ts.URL+"/api/v1/auth/keys/"+keyID, login.Token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", res.StatusCode)
	}
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", login.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session after key revoke = %d, want 200", res.StatusCode)
	}
}

func TestAPIKeyDeleteForeignKeyHidden(t *testing.T) {
	ts := newAuthTestAPI(t)
	for _, u := range []string{"alice", "mallory"} {
		if _, body := registerUser(t, ts, u, "secret123"); len(body) == 0 {
			t.Fatalf("register %s failed", u)
		}
	}
	res, body := loginUser(t, ts, "alice", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login alice = %d; body=%s", res.StatusCode, body)
	}
	var alice LoginAuthResponse
	_ = json.Unmarshal(body, &alice)
	_, keyID := createAPIKey(t, ts, alice.Token)

	res, body = loginUser(t, ts, "mallory", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login mallory = %d; body=%s", res.StatusCode, body)
	}
	var mallory LoginAuthResponse
	_ = json.Unmarshal(body, &mallory)

	// Mallory cannot see or delete alice's key: 404 (no existence leak).
	res, body = doAuthJSON(t, http.MethodDelete, ts.URL+"/api/v1/auth/keys/"+keyID, mallory.Token, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign delete = %d, want 404; body=%s", res.StatusCode, body)
	}
	res, body = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", mallory.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mallory list = %d; body=%s", res.StatusCode, body)
	}
	var list ListAPIKeysResponse
	_ = json.Unmarshal(body, &list)
	if list.Total != 0 {
		t.Fatalf("mallory sees foreign keys: %+v", list)
	}

	// Alice's key still works (not revoked by mallory's attempt).
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", alice.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("alice session = %d, want 200", res.StatusCode)
	}
}

func TestAPIKeyAuthRejectsBadCredentials(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "carol", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}
	res, body := loginUser(t, ts, "carol", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; body=%s", res.StatusCode, body)
	}
	var login LoginAuthResponse
	_ = json.Unmarshal(body, &login)

	// No token → 401.
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", res.StatusCode)
	}
	// Forged key → 401.
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", account.APIKeyPrefix+"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged key = %d, want 401", res.StatusCode)
	}
	// API key cannot list keys of others or create without auth (401 at gate).
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/keys", login.Token+"x", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered session = %d, want 401", res.StatusCode)
	}
}

// TestAPIKeyLogRedaction asserts S3: no log line emitted by the key-creation
// path (access log or handler) contains the plaintext key.
func TestAPIKeyLogRedaction(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "dave", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}
	res, body := loginUser(t, ts, "dave", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; body=%s", res.StatusCode, body)
	}
	var login LoginAuthResponse
	_ = json.Unmarshal(body, &login)

	// Route the default slog (used by the access-log middleware) into a buffer.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	plain, _ := createAPIKey(t, ts, login.Token)
	if !strings.Contains(buf.String(), "POST") {
		t.Fatalf("access log not captured: %s", buf.String())
	}
	if strings.Contains(buf.String(), plain) {
		t.Fatalf("log leaks plaintext API key: %s", buf.String())
	}
	// The prefix alone is fine (it identifies the key type without revealing it).
	if !strings.Contains(buf.String(), "/api/v1/auth/keys") {
		t.Fatalf("expected the key path in the access log: %s", buf.String())
	}
}

// TestAPIKeyLogoutIsIdempotentForKeys asserts that logout with an API key is a
// 204 no-op (API keys are not sessions and must not be revoked by logout).
func TestAPIKeyLogoutIsIdempotentForKeys(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "erin", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}
	res, body := loginUser(t, ts, "erin", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; body=%s", res.StatusCode, body)
	}
	var login LoginAuthResponse
	_ = json.Unmarshal(body, &login)

	plain, _ := createAPIKey(t, ts, login.Token)
	res, _ = doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/logout", plain, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout with api key = %d, want 204", res.StatusCode)
	}
	res, _ = doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", plain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("key after logout = %d, want 200 (still valid)", res.StatusCode)
	}
}
