package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/account"
)

// newAuthRequiredTestAPI starts an API in AUTH mode (no WithAPIKeyDisabled),
// so requireAuth actually enforces Bearer tokens.
func newAuthRequiredTestAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	api := NewAPI(0, WithDataDir(t.TempDir()))
	ts := httptest.NewServer(api.httpServer.Handler)
	t.Cleanup(ts.Close)
	return api, ts
}

// loginToken registers a dev user and logs in, returning its bearer token.
func loginToken(t *testing.T, ts *httptest.Server, username, password string) string {
	t.Helper()
	if res, _ := registerUser(t, ts, username, password); res.StatusCode != http.StatusCreated &&
		res.StatusCode != http.StatusConflict {
		t.Fatalf("register %s = %d", username, res.StatusCode)
	}
	_, body := loginUser(t, ts, username, password)
	var lr LoginAuthResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("unmarshal login: %v; body=%s", err, body)
	}
	return lr.Token
}

func TestRequireAuthNoAuthModeBypasses(t *testing.T) {
	// --no-auth smoke mode: Web routes are reachable without a token.
	ts := newAuthTestAPI(t)
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", "", nil)
	if res.StatusCode == http.StatusUnauthorized {
		t.Fatalf("no-auth mode should bypass requireAuth, got 401; body=%s", body)
	}
}

func TestRequireAuthAuthModeRejectsNoToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /devices without token = %d, want 401; body=%s", res.StatusCode, body)
	}
}

func TestRequireAuthAuthModeRejectsBadToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", "not-a-real-token", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /devices with bad token = %d, want 401; body=%s", res.StatusCode, body)
	}
}

func TestRequireAuthValidTokenPasses(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginToken(t, ts, "dev1", "secret123")
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /devices with dev token = %d, want 200; body=%s", res.StatusCode, body)
	}
}

func TestRequireAuthLogoutRevokesToken(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginToken(t, ts, "dev2", "secret123")
	// Sanity: token works first.
	if res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", tok, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("pre-logout want 200, got %d", res.StatusCode)
	}
	// Logout revokes server-side.
	if res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/auth/logout", tok, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", res.StatusCode)
	}
	res, body := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", tok, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout GET /devices = %d, want 401; body=%s", res.StatusCode, body)
	}
}

func TestRequireAuthExpiredSessionRejected(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	// Forge an already-expired session directly in the store.
	sess := &account.AuthSession{
		Token:     "expired-token-xyz",
		Username:  "ghost",
		CreatedAt: time.Now().Add(-2 * authSessionTTL),
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := api.authSessions.Create(t.Context(), sess); err != nil {
		t.Fatalf("seed expired session: %v", err)
	}
	res, _ := doAuthJSON(t, http.MethodGet, ts.URL+"/api/v1/devices", "expired-token-xyz", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token = %d, want 401", res.StatusCode)
	}
}

func TestSDKRoutesStayOpen(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	// SDK-facing routes are deliberately NOT wrapped by requireAuth: the SDK
	// sends no credentials. register must not be blocked by 401 (it returns
	// its own business status: 201 on success).
	res, body := registerUser(t, ts, "sdkuser", "secret123")
	if res.StatusCode == http.StatusUnauthorized {
		t.Fatalf("SDK/register must stay open, got 401; body=%s", body)
	}
}

func TestRequireRoleChannel(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)

	// Seed an admin directly (CLI path equivalent).
	if err := api.CreateAdminUser(t.Context(), "root", "adminpass1"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	adminTok := loginToken(t, ts, "root", "adminpass1")
	devTok := loginToken(t, ts, "dev3", "secret123")

	probe := func(tok string) int {
		h := api.requireRole(account.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := probe(adminTok); c != http.StatusOK {
		t.Fatalf("admin under requireRole(admin) = %d, want 200", c)
	}
	if c := probe(devTok); c != http.StatusForbidden {
		t.Fatalf("dev under requireRole(admin) = %d, want 403", c)
	}
	if c := probe(""); c != http.StatusUnauthorized {
		t.Fatalf("no token under requireRole(admin) = %d, want 401", c)
	}
}
