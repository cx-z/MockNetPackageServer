// MockNetPack session / traffic / share API handlers (pure move from
// capture_handlers.go): session lifecycle and viewer leases, traffic upload,
// query, delete/clear, and M8.5 share links.
package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/getmockd/mockd/pkg/store"
)

// handleActivateSession handles POST /api/v1/sessions (Web「连接」).
func (a *API) handleActivateSession(w http.ResponseWriter, r *http.Request) {
	var req ActivateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.App == "" || req.Did == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app and did are required")
		return
	}
	if !a.authorizeDeviceAccess(w, r, req.App, req.Did) {
		return
	}

	session, created, err := a.captureManager.ActivateSession(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, session)
}

// handleListSessions handles GET /api/v1/sessions?app=&did=.
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	app := q.Get("app")
	did := q.Get("did")
	if app == "" || did == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app and did query parameters are required (device isolation)")
		return
	}

	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	filter := &store.SessionFilter{App: &app, Did: &did}
	sessions, err := a.captureManager.ListSessions(r.Context(), filter)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: sessions, Total: len(sessions)})
}

// handleGetSession handles GET /api/v1/sessions/{id}.
func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sid, ok := a.authorizeSessionAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sid)
}

// handleEndSession handles DELETE /api/v1/sessions/{id} (Web「断开」).
func (a *API) handleEndSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.EndSession(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRegisterViewer handles POST /api/v1/sessions/{id}/viewers.
func (a *API) handleRegisterViewer(w http.ResponseWriter, r *http.Request) {
	var req RegisterViewerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.ViewerID == "" {
		writeError(w, http.StatusBadRequest, "missing_viewer_id", "viewerId is required")
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}

	lease, err := a.captureManager.RegisterViewer(r.Context(), r.PathValue("id"), req.ViewerID, req.Label)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleReleaseViewer handles DELETE /api/v1/sessions/{id}/viewers/{viewerId}.
func (a *API) handleReleaseViewer(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.ReleaseViewer(r.Context(), r.PathValue("id"), r.PathValue("viewerId")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadTraffic handles POST /api/v1/traffic (SDK batch upload, 全量抓包).
// Contract semantics: 202 on acceptance (partial acceptance allowed — count is
// the number of entries stored); 400 when the batch is empty or every entry is
// invalid; 404 session_not_found for an unknown session; 409 session_ended for
// an ended session.
func (a *API) handleUploadTraffic(w http.ResponseWriter, r *http.Request) {
	var req TrafficUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.App == "" || req.Did == "" || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "app, did and sessionId are required")
		return
	}
	if len(req.Entries) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "entries must not be empty")
		return
	}
	if len(req.Entries) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "entries must be at most 500 items")
		return
	}

	count, err := a.captureManager.UploadTraffic(r.Context(), req.App, req.Did, req.SessionID, req.Entries)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if count == 0 {
		// Every entry was invalid (empty method/url or zero timestamp).
		writeError(w, http.StatusBadRequest, "invalid_field", "all traffic entries are invalid")
		return
	}
	writeJSON(w, http.StatusAccepted, TrafficUploadResponse{Accepted: true, Count: count})
}

// handleGetTraffic handles GET /api/v1/traffic/{id} (single request detail).
// The entry may live in an active session or in the retained store of an ended
// session (M8.6); ownership is checked against the owning device either way.
func (a *API) handleGetTraffic(w http.ResponseWriter, r *http.Request) {
	app, did, err := a.captureManager.GetTrafficWithOwner(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	entry, err := a.captureManager.GetTraffic(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleListSessionTraffic handles GET /api/v1/sessions/{id}/traffic (Web
// request stream — the 2s-polling push channel; no extra push endpoint is
// needed, decision D-M2-1). limit defaults to 100 and must be 1..500 (0 means
// default); offset defaults to 0.
func (a *API) handleListSessionTraffic(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be an integer")
		return
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be between 1 and 500")
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be an integer")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be non-negative")
		return
	}

	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	entries, total, err := a.captureManager.ListSessionTraffic(r.Context(), r.PathValue("id"), limit, offset)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TrafficListResponse{Entries: entries, Total: total})
}

// handleDeleteTraffic handles DELETE /api/v1/traffic/{id} (Web per-row
// "删除"; M9.5). 204 on success; 404 not_found for an unknown entry — the Web
// treats delete as best-effort (the entry may belong to an already-deleted
// session).
func (a *API) handleDeleteTraffic(w http.ResponseWriter, r *http.Request) {
	entry, err := a.captureManager.GetTraffic(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if _, ok := a.authorizeSessionAccess(w, r, entry.SessionID); !ok {
		return
	}
	if err := a.captureManager.DeleteTraffic(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClearSessionTraffic handles DELETE /api/v1/sessions/{id}/traffic
// (Web "清空日志"; M9.5). 204 on success; 404 session_not_found for an unknown
// session.
func (a *API) handleClearSessionTraffic(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	if err := a.captureManager.ClearSessionTraffic(r.Context(), r.PathValue("id")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateShare handles POST /api/v1/shares (authenticated). It snapshots
// a single traffic entry into an independent share store so the share survives
// session/traffic deletion. The entry may live in an active session or in the
// retained store of an ended session (M8.6 断开后可分享); in both cases the
// caller must own the entry's device.
func (a *API) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req shareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "request body must be JSON")
		return
	}
	if req.TrafficID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "trafficId is required")
		return
	}
	// Resolve the owning device: for an ended session the session record is
	// gone (M9), so ownership must come from the retained store instead of
	// authorizeSessionAccess.
	app, did, err := a.captureManager.GetTrafficWithOwner(r.Context(), req.TrafficID)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	snap, err := a.captureManager.CreateShare(r.Context(), req.TrafficID)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, shareResponse{
		ShareID:   snap.ShareID,
		URL:       "/mocknetpack/#/share/" + snap.ShareID,
		ExpiresAt: snap.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleGetShare handles GET /api/v1/shares/{id} (public, no auth). Returns the
// read-only snapshot. Expired or unknown shares return 404.
func (a *API) handleGetShare(w http.ResponseWriter, r *http.Request) {
	snap, err := a.captureManager.GetShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
