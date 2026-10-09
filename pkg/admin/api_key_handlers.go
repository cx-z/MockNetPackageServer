// Package admin implements the MockNetPack admin API.
//
// Long-lived API key endpoints: create / list / revoke. Keys are machine
// credentials for scripts and Agent tooling — they
// authenticate through the same `Authorization: Bearer <key>` header as
// session tokens (see auth_middleware.go) and outlive the 7-day login token.
//
// Security contract (S3): the plaintext key is returned exactly once, at
// creation; every other path (list, store, logs) carries only the hash or a
// recognizable prefix. No log line in this file (or the auth middleware)
// prints the key.
package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/store"
)

// APIKeyView is the list/delete shape of an API key (no plaintext, no hash —
// only the recognizable prefix and lifecycle fields).
type APIKeyView struct {
	ID        string     `json:"id"`
	KeyPrefix string     `json:"keyPrefix"`
	Username  string     `json:"username"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// CreateAPIKeyResponse is the creation payload — the ONLY response that ever
// carries the plaintext `key`.
type CreateAPIKeyResponse struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// ListAPIKeysResponse is the list payload (keys never contain plaintext).
type ListAPIKeysResponse struct {
	Keys  []APIKeyView `json:"keys"`
	Total int          `json:"total"`
}

// handleCreateAPIKey handles POST /api/v1/auth/keys — issues a long-lived
// machine credential for the current user. The plaintext key is returned in
// this response and never again.
func (a *API) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	now := time.Now()
	key, plain, err := account.NewAPIKey(u.Username, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	if err := a.apiKeys.Create(r.Context(), key); err != nil {
		switch {
		case errors.Is(err, store.ErrReadOnly):
			writeError(w, http.StatusConflict, "read_only", "Store is read-only")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		}
		return
	}
	var exp *time.Time
	if !key.ExpiresAt.IsZero() {
		e := key.ExpiresAt
		exp = &e
	}
	// Success: the plaintext leaves the server exactly once, in this body.
	writeJSON(w, http.StatusCreated, CreateAPIKeyResponse{
		ID:        key.ID,
		Key:       plain,
		CreatedAt: key.CreatedAt,
		ExpiresAt: exp,
	})
}

// handleListAPIKeys handles GET /api/v1/auth/keys — every key of the current
// user, with no plaintext and no hash.
func (a *API) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	keys, err := a.apiKeys.ListByUsername(r.Context(), u.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	views := make([]APIKeyView, 0, len(keys))
	for _, k := range keys {
		var exp *time.Time
		if !k.ExpiresAt.IsZero() {
			e := k.ExpiresAt
			exp = &e
		}
		views = append(views, APIKeyView{
			ID:        k.ID,
			KeyPrefix: k.KeyPrefix,
			Username:  k.Username,
			CreatedAt: k.CreatedAt,
			ExpiresAt: exp,
		})
	}
	writeJSON(w, http.StatusOK, ListAPIKeysResponse{Keys: views, Total: len(views)})
}

// handleDeleteAPIKey handles DELETE /api/v1/auth/keys/{id} — immediate
// revocation. Users may revoke only their own keys; an unknown or
// foreign-owned key surfaces as 404 (no existence leak, same convention as
// device ownership).
func (a *API) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	k, err := a.apiKeys.GetByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	// Ownership: only the key's owner (or an admin) may revoke it. Others see
	// 404 — the existence of someone else's key is not revealed.
	if k.Username != u.Username && u.Role != account.RoleAdmin {
		writeError(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	if err := a.apiKeys.Delete(r.Context(), k.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "API key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", ErrMsgInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
