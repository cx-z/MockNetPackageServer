// Package admin implements the MockNetPack mock-rule API handlers: Web
// CRUD / toggle and the SDK incremental rule snapshot pull. Routes live under
// /api/v1/devices/{app}/{did}.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// MockRuleListResponse is the contract MockRuleList: the device's rule set plus
// the current monotonic version. Conflicts is only populated for the Web full
// list (omitted on the SDK incremental pull).
type MockRuleListResponse struct {
	Version   int                        `json:"version"`
	Rules     []*capture.MockRuleView    `json:"rules"`
	Conflicts []capture.MockRuleConflict `json:"conflicts,omitempty"`
}

// ruleCaller builds the  permission identity for a rule mutation from the
// authenticated request. nil under --no-auth (smoke mode): no session user.
func ruleCaller(r *http.Request) *store.RuleCaller {
	u := currentUser(r)
	if u == nil {
		return nil
	}
	return &store.RuleCaller{Username: u.Username, IsAdmin: u.Role == account.RoleAdmin}
}

// sdkRuleView is the SDK incremental-pull wire format : only the fields
// the SDK consumes. owner/updatedBy (and other Web-only fields such as
// source/note/lastUsedAt) are pure server-side fields and must never reach the
// SDK — the contract guarantees the pull leaks nothing beyond the match key,
// the canned response, the switch and the runtime effective flag.
type sdkRuleView struct {
	ID        string               `json:"id"`
	Method    string               `json:"method"`
	Path      string               `json:"path"`
	Response  capture.MockResponse `json:"response"`
	Enabled   bool                 `json:"enabled"`
	Effective bool                 `json:"effective"`
}

// toSDKRules maps full views to the SDK wire format.
func toSDKRules(views []*capture.MockRuleView) []sdkRuleView {
	out := make([]sdkRuleView, 0, len(views))
	for _, v := range views {
		out = append(out, sdkRuleView{
			ID:        v.ID,
			Method:    v.Method,
			Path:      v.Path,
			Response:  v.Response,
			Enabled:   v.Enabled,
			Effective: v.Effective,
		})
	}
	return out
}

// sdkRuleListResponse is the SDK pull response shape: version plus the stripped
// rule set. Distinct from MockRuleListResponse so the wire format for the SDK
// can never accidentally grow Web-only fields.
type sdkRuleListResponse struct {
	Version int           `json:"version"`
	Rules   []sdkRuleView `json:"rules"`
}

// handleListMockRules handles GET /api/v1/devices/{app}/{did}/mock-rules.
//
// Two consumers share one route (contract listMockRules):
//   - SDK incremental pull: ?sinceVersion=N returns only Effective rules. When
//     the device version is unchanged the list is empty (SDK keeps its snapshot).
//   - Web full list: no sinceVersion returns every rule (including disabled,
//     with source snapshots) plus the abnormal conflicts report.
func (a *API) handleListMockRules(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")

	raw := r.URL.Query().Get("sinceVersion")
	if raw != "" {
		// SDK incremental pull ( ): SDK channel like register/heartbeat/traffic
		// upload — no Web Bearer auth. SDK identity is (app, did); unknown-did rejection
		// lands in ListActiveMockRules.
		since, err := strconv.Atoi(raw)
		if err != nil || since < 0 {
			writeError(w, http.StatusBadRequest, "invalid_field", "sinceVersion must be a non-negative integer")
			return
		}
		active, version, changed, err := a.captureManager.ListActiveMockRules(r.Context(), app, did, since)
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		// SDK wire format: strip owner/updatedBy and other Web-only fields
		// ( — pure server-side fields must not leave the server).
		sdkRules := toSDKRules(active)
		if sdkRules == nil {
			sdkRules = []sdkRuleView{}
		}
		writeJSON(w, http.StatusOK, sdkRuleListResponse{
			Version: version,
			Rules:   sdkRules,
		})
		_ = changed // always true when version != since; empty array is the "no change" signal to the SDK
		return
	}

	// Web full list: require a logged-in Bearer token (route is open for the SDK
	// incremental pull, so auth is enforced here, per-branch). In auth mode a
	// missing/invalid token is 401. In --no-auth smoke mode the token is
	// optional: a valid one still injects the caller identity , otherwise
	// the request proceeds with caller=nil — identical to requireAuth, so the
	// GET full list and PUT/DELETE never disagree on the caller.
	u := a.authenticate(r)
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
	} else if a.apiKeyConfig.Enabled {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}

	views, conflicts, version, err := a.captureManager.ListMockRules(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if views == nil {
		views = []*capture.MockRuleView{}
	}
	writeJSON(w, http.StatusOK, MockRuleListResponse{
		Version:   version,
		Rules:     views,
		Conflicts: conflicts,
	})
}

// handleCreateMockRule handles POST /api/v1/devices/{app}/{did}/mock-rules.
// Creates a rule (default disabled). Enabling it rejects with 409 if another
// enabled rule already matches the same Method+Path.
func (a *API) handleCreateMockRule(w http.ResponseWriter, r *http.Request) {
	var in capture.MockRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if !validateMockRuleInput(w, &in) {
		return
	}

	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	view, _, err := a.captureManager.CreateMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), &in, ruleCaller(r))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// handleUpdateMockRule handles PUT /api/v1/devices/{app}/{did}/mock-rules/{ruleId}.
// Edits the canned response and note (match key Method+Path is immutable);
// optionally flips the enabled switch. Enabling against another enabled rule on
// the same interface returns 409. note must be non-blank only when the PUT
// actually changes the canned response ; a pure toggle may leave it blank.
func (a *API) handleUpdateMockRule(w http.ResponseWriter, r *http.Request) {
	var in capture.UpdateMockRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if !validStatusCode(in.Response.StatusCode) {
		writeInvalidStatusCode(w, in.Response.StatusCode)
		return
	}

	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	view, _, err := a.captureManager.UpdateMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId"), &in, ruleCaller(r))
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDeleteMockRule handles DELETE /api/v1/devices/{app}/{did}/mock-rules/{ruleId}.
func (a *API) handleDeleteMockRule(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeDeviceAccess(w, r, r.PathValue("app"), r.PathValue("did")) {
		return
	}
	if _, err := a.captureManager.DeleteMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId"), ruleCaller(r)); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateMockRuleInput enforces the contract required fields (MockRuleInput:
// method, path, response.statusCode). Returns false and writes a 400 when
// invalid.
//
//	Step1: path must start with '/' and must not contain '?' — matching is
//
// an exact Method+path comparison (), so a query string pasted into
// path could never hit and would silently drift. Applies uniformly to both
// creation paths (评审结论 #2): capture-originated paths come from URL.path
// and already satisfy this, so there is no regression.
func validateMockRuleInput(w http.ResponseWriter, in *capture.MockRuleInput) bool {
	if in.Method == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "method is required")
		return false
	}
	if in.Path == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "path is required")
		return false
	}
	if !strings.HasPrefix(in.Path, "/") || strings.Contains(in.Path, "?") {
		writeError(w, http.StatusBadRequest, "invalid_field", "path must start with '/' and must not contain '?' (query strings do not participate in matching)")
		return false
	}
	if !validStatusCode(in.Response.StatusCode) {
		writeInvalidStatusCode(w, in.Response.StatusCode)
		return false
	}
	return true
}

// writeInvalidStatusCode returns a 400 with a diagnosis that names the failing
// field and the phase (create vs. update) so MCP/CLI callers can self-correct.
// A zero value on PUT means the caller did not send a full canned response —
// the update is a full replace (/), so a pure toggle must re-send the
// complete response (read-modify-write).
func writeInvalidStatusCode(w http.ResponseWriter, code int) {
	if code == 0 {
		writeError(w, http.StatusBadRequest, "invalid_field",
			"response.statusCode 缺失或为 0：PUT 为整体覆盖更新，必须携带完整 response（statusCode 100–599）。纯启停请先查询规则（GET mock-rules）再整体回写")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_field",
		"response.statusCode="+strconv.Itoa(code)+" 不在合法范围 100–599")
}

// validStatusCode reports whether code is a legal HTTP status code (4.17):
// the contract previously accepted any value > 0, so 0/negative fell out but
// garbage like 99 or 700 could be stored and would never match a real response.
func validStatusCode(code int) bool {
	return code >= 100 && code <= 599
}
