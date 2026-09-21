package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// newAuthTestAPI starts an API with a fresh temp data dir and no API key,
// ready for account-system tests.
func newAuthTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	api := NewAPI(0, WithDataDir(t.TempDir()), WithAPIKeyDisabled())
	ts := httptest.NewServer(api.httpServer.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func doAuthJSON(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func registerUser(t *testing.T, ts *httptest.Server, username, password string) (*http.Response, []byte) {
	t.Helper()
	return doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/register", "", map[string]string{
		"username": username, "password": password,
	})
}

func loginUser(t *testing.T, ts *httptest.Server, username, password string) (*http.Response, []byte) {
	t.Helper()
	return doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/login", "", map[string]string{
		"username": username, "password": password,
	})
}

func TestAuthRegister(t *testing.T) {
	ts := newAuthTestAPI(t)

	// 201: registration creates a dev account; response must not expose the
	// password hash.
	res, body := registerUser(t, ts, "alice", "secret123")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201; body=%s", res.StatusCode, body)
	}
	var u AuthUser
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.Username != "alice" || u.Role != account.RoleDev {
		t.Fatalf("registered user = %+v, want alice/dev", u)
	}
	if strings.Contains(string(body), "passwordHash") || strings.Contains(string(body), "PasswordHash") {
		t.Fatalf("response leaks password hash: %s", body)
	}

	// 409: duplicate username.
	res, body = registerUser(t, ts, "alice", "another1")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate register = %d, want 409; body=%s", res.StatusCode, body)
	}

	// 400: invalid credentials.
	for _, c := range []struct{ u, p string }{
		{"", "secret123"},       // empty username
		{"bob", "123"},          // password too short
		{"sp ace", "secret123"}, // whitespace in username
		{"bob", ""},             // empty password
	} {
		res, _ := registerUser(t, ts, c.u, c.p)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("register(%q,%q) = %d, want 400", c.u, c.p, res.StatusCode)
		}
	}
}

func TestAuthLogin(t *testing.T) {
	ts := newAuthTestAPI(t)
	if _, body := registerUser(t, ts, "bob", "secret123"); len(body) == 0 {
		t.Fatal("register failed")
	}

	// 200: correct credentials issue a 64-char token and the user.
	res, body := loginUser(t, ts, "bob", "secret123")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200; body=%s", res.StatusCode, body)
	}
	var lr LoginAuthResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lr.Token) != 64 {
		t.Fatalf("token len = %d, want 64", len(lr.Token))
	}
	if lr.User.Username != "bob" || lr.User.Role != account.RoleDev {
		t.Fatalf("login user = %+v", lr.User)
	}

	// 401: wrong password and unknown user are indistinguishable.
	for _, c := range []struct{ u, p string }{
		{"bob", "wrongpass"},
		{"nobody", "secret123"},
	} {
		res, body := loginUser(t, ts, c.u, c.p)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login(%q) = %d, want 401; body=%s", c.u, res.StatusCode, body)
		}
	}

	// 400: missing fields.
	if res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/login", "", map[string]string{"username": "bob"}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("login missing password = %d, want 400", res.StatusCode)
	}
}

func TestAuthMe(t *testing.T) {
	ts := newAuthTestAPI(t)
	registerUser(t, ts, "carol", "secret123")
	_, body := loginUser(t, ts, "carol", "secret123")
	var lr LoginAuthResponse
	_ = json.Unmarshal(body, &lr)

	// 200 with valid token.
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", lr.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me = %d, want 200; body=%s", res.StatusCode, body)
	}
	var u AuthUser
	_ = json.Unmarshal(body, &u)
	if u.Username != "carol" || u.Role != account.RoleDev {
		t.Fatalf("me user = %+v", u)
	}

	// 401 without token and with garbage token.
	if res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me (no token) = %d, want 401", res.StatusCode)
	}
	if res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", "garbage", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me (garbage) = %d, want 401", res.StatusCode)
	}
}

func TestAuthLogoutRevokes(t *testing.T) {
	ts := newAuthTestAPI(t)
	registerUser(t, ts, "dave", "secret123")
	_, body := loginUser(t, ts, "dave", "secret123")
	var lr LoginAuthResponse
	_ = json.Unmarshal(body, &lr)

	// 204 on logout.
	res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/logout", lr.Token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", res.StatusCode)
	}

	// The revoked token is immediately invalid (server-side revocation).
	if res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", lr.Token, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", res.StatusCode)
	}

	// Logout without token is 401.
	if res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/logout", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout (no token) = %d, want 401", res.StatusCode)
	}
}

func TestCreateAdminUser(t *testing.T) {
	api := NewAPI(0, WithDataDir(t.TempDir()), WithAPIKeyDisabled())
	ts := httptest.NewServer(api.httpServer.Handler)
	defer ts.Close()

	// CLI path: CreateAdminUser.
	if err := api.CreateAdminUser(t.Context(), "root", "admin-secret"); err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	// Duplicate is ErrAlreadyExists.
	if err := api.CreateAdminUser(t.Context(), "root", "other-secret"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate CreateAdminUser = %v, want ErrAlreadyExists", err)
	}
	// Invalid credentials rejected.
	if err := api.CreateAdminUser(t.Context(), "x", "123"); err == nil {
		t.Fatal("CreateAdminUser with short password should fail")
	}

	// Admin can log in and /me reports role=admin.
	_, body := loginUser(t, ts, "root", "admin-secret")
	var lr LoginAuthResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("admin login unmarshal: %v (body=%s)", err, body)
	}
	if lr.User.Role != account.RoleAdmin {
		t.Fatalf("admin role = %q, want admin", lr.User.Role)
	}
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/auth/me", lr.Token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin me = %d, want 200; body=%s", res.StatusCode, body)
	}
}

func TestRegisterNeverProducesAdmin(t *testing.T) {
	ts := newAuthTestAPI(t)

	// Even if the client smuggles a role field, the API ignores it: open
	// registration always creates dev accounts (contract v0.6.0).
	res, body := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/register", "", map[string]any{
		"username": "eve", "password": "secret123", "role": "admin",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201; body=%s", res.StatusCode, body)
	}
	var u AuthUser
	_ = json.Unmarshal(body, &u)
	if u.Role != account.RoleDev {
		t.Fatalf("role = %q, want dev", u.Role)
	}
}
