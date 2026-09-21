// Bearer-token auth middleware for the Web-facing MockNetPack routes (M7.1.3).
//
// Scope decision (M7 拍板 #6): Web management routes (device list/detail,
// session control, viewer leases, traffic query/delete, mock-rule CRUD)
// require a valid logged-in user; SDK-facing routes (device register, device
// heartbeat, traffic upload) are intentionally NOT wrapped — the SDK carries
// no credentials, its identity is (app, did), and unknown-did rejection
// lands separately in M7.2.3.
//
// --no-auth (apiKeyConfig disabled) bypasses this middleware entirely for
// local smoke tests; on real-device setups it is omitted and the Bearer
// requirement is enforced.

package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/getmockd/mockd/pkg/account"
)

// userCtxKey carries the authenticated user inside request context.
type userCtxKey struct{}

// UserCtx is the authenticated caller resolved by requireAuth.
type UserCtx struct {
	Username string
	Role     account.Role
}

// currentUser returns the authenticated caller injected by requireAuth, or
// nil when the request bypassed auth (--no-auth smoke mode) or the route is
// public.
func currentUser(r *http.Request) *UserCtx {
	v, _ := r.Context().Value(userCtxKey{}).(*UserCtx)
	return v
}

// requireAuth wraps a Web-facing handler with Bearer-token verification. A
// valid token resolves to an AuthSession + User which is injected into the
// request context. Missing/invalid/expired tokens get 401; role-based
// 403 checks are opt-in via requireRole for future admin-only routes.
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// --no-auth: local smoke mode (same switch as the legacy API-key flag).
		if !a.apiKeyConfig.Enabled {
			next(w, r)
			return
		}
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
		ctx := context.WithValue(r.Context(), userCtxKey{}, &UserCtx{
			Username: u.Username,
			Role:     u.Role,
		})
		next(w, r.WithContext(ctx))
	}
}

// requireRole wraps requireAuth and additionally demands one of the given
// roles (403 otherwise). M7.1.3 ships the channel; no route is admin-only yet
// (app management is a recorded backlog item, owner filtering lands M7.2.2).
func (a *API) requireRole(role account.Role, next http.HandlerFunc) http.HandlerFunc {
	return a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil || u.Role != role {
			writeError(w, http.StatusForbidden, "forbidden", "insufficient role")
			return
		}
		next(w, r)
	})
}
