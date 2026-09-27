// MockNetPack account system (M7.1, contract v0.6.0): open registration (dev
// only), login (server-issued session token), logout (server-side revocation)
// and the current-user endpoint. All routes live under /api/v1/auth.
//
// The admin role is NOT registered here — the only creation path is the CLI
// `mockd start --create-admin` (M7.1 拍板). Role-based access control (403)
// and wiring auth into the existing device/session/traffic/rule endpoints land
// in M7.1.3; this milestone delivers the account model, the auth API and the
// session-token lifecycle.

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// authSessionTTL is how long a server-issued session token stays valid before
// expiring. Fixed 7 days, no sliding renewal (M7.1 拍板: 服务端吊销 + 7 天).
const authSessionTTL = 7 * 24 * time.Hour

// AuthUser is the API output shape for an account (contract User schema):
// the password hash never leaves the server.
type AuthUser struct {
	Username  string       `json:"username"`
	Role      account.Role `json:"role"`
	CreatedAt time.Time    `json:"createdAt"`
}

// RegisterAuthRequest is the open-registration payload (contract schema).
type RegisterAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginAuthRequest is the login payload (contract schema).
type LoginAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginAuthResponse is the login success payload (contract schema).
type LoginAuthResponse struct {
	Token string   `json:"token"`
	User  AuthUser `json:"user"`
}

// handleAuthRegister handles POST /api/v1/auth/register. Open registration
// always creates a dev account; admins cannot be registered here.
func (a *API) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if msg := validateCredentials(req.Username, req.Password); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_field", msg)
		return
	}

	hash, err := account.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	u := &account.User{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         account.RoleDev,
		CreatedAt:    time.Now(),
	}
	if err := a.users.Create(r.Context(), u); err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "username_taken", "username already exists")
		case errors.Is(err, store.ErrReadOnly):
			writeError(w, http.StatusConflict, "read_only", "Store is read-only")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		}
		return
	}
	writeJSON(w, http.StatusCreated, toAuthUser(u))
}

// handleAuthLogin handles POST /api/v1/auth/login. Successful login issues a
// server-persisted session token (7-day TTL); logout revokes it server-side.
func (a *API) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "username and password are required")
		return
	}

	// 4.16: failure-based lockout (per-username, and per-IP for non-loopback
	// sources). Checked before the user lookup so throttled callers neither
	// consume PBKDF2 cost nor get a timing signal about lockout state.
	ip := clientIP(r)
	if !a.loginThrottle.allow(req.Username, ip) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed login attempts, try again later")
		return
	}

	u, err := a.users.GetByUsername(r.Context(), req.Username)
	if err != nil {
		// 4.16: constant-time decoy — run the same PBKDF2 cost on a dummy hash
		// so an unknown username takes exactly as long as a known one (no
		// username-enumeration timing oracle).
		account.VerifyPassword(account.DummyPasswordHash(), req.Password)
		a.loginThrottle.recordFailure(req.Username, ip)
		// Identical 401 for unknown user and wrong password (no user
		// enumeration, contract: 401 invalid_credentials).
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if !account.VerifyPassword(u.PasswordHash, req.Password) {
		a.loginThrottle.recordFailure(req.Username, ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	a.loginThrottle.recordSuccess(req.Username, ip)

	token, err := account.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	now := time.Now()
	sess := &account.AuthSession{
		Token:     token,
		Username:  u.Username,
		CreatedAt: now,
		ExpiresAt: now.Add(authSessionTTL),
	}
	if err := a.authSessions.Create(r.Context(), sess); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	writeJSON(w, http.StatusOK, LoginAuthResponse{Token: token, User: toAuthUser(u)})
}

// handleAuthLogout handles POST /api/v1/auth/logout — server-side revocation
// of the presented token: the session record is deleted, so the token is
// immediately invalid even if it lingers in the client.
func (a *API) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if _, err := a.authSessions.GetByToken(r.Context(), token); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if err := a.authSessions.Delete(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthMe handles GET /api/v1/auth/me — the current user behind the
// presented token, for the Web to decide login state and role.
func (a *API) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	sess, err := a.authSessions.GetByToken(r.Context(), token)
	if err != nil || !sess.Valid(time.Now()) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	u, err := a.users.GetByUsername(r.Context(), sess.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	writeJSON(w, http.StatusOK, toAuthUser(u))
}

// CreateAdminUser creates an admin account — the only admin creation path
// (CLI `mockd start --create-admin <user> --admin-password <pass>`, M7.1
// 拍板). Returns store.ErrAlreadyExists when the username is taken.
func (a *API) CreateAdminUser(ctx context.Context, username, password string) error {
	if msg := validateCredentials(username, password); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	hash, err := account.HashPassword(password)
	if err != nil {
		return err
	}
	u := &account.User{
		Username:     username,
		PasswordHash: hash,
		Role:         account.RoleAdmin,
		CreatedAt:    time.Now(),
	}
	return a.users.Create(ctx, u)
}

// bearerToken extracts the Bearer token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.Fields(h)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// validateCredentials returns a non-empty message when the credentials are
// invalid (contract: username 1~64 chars without whitespace; password 6~128).
func validateCredentials(username, password string) string {
	if username == "" || len(username) > 64 || strings.ContainsAny(username, " \t\r\n") {
		return "username must be 1-64 characters without whitespace"
	}
	if len(password) < 6 || len(password) > 128 {
		return "password must be 6-128 characters"
	}
	return ""
}

// toAuthUser maps an account.User to the API shape (password hash omitted).
func toAuthUser(u *account.User) AuthUser {
	return AuthUser{Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt}
}
