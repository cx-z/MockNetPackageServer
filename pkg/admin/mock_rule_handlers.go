// MockNetPack mock-rule API handlers (M3): Web CRUD / toggle and the SDK
// incremental rule snapshot pull. Routes live under /api/v1/devices/{app}/{did}.
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/getmockd/mockd/pkg/capture"
)

// MockRuleListResponse is the contract MockRuleList: the device's rule set plus
// the current monotonic version. Conflicts is only populated for the Web full
// list (omitted on the SDK incremental pull).
type MockRuleListResponse struct {
	Version  int                        `json:"version"`
	Rules    []*capture.MockRuleView    `json:"rules"`
	Conflicts []capture.MockRuleConflict `json:"conflicts,omitempty"`
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
		rules := active
		if rules == nil {
			rules = []*capture.MockRuleView{}
		}
		writeJSON(w, http.StatusOK, MockRuleListResponse{
			Version: version,
			Rules:   rules,
		})
		_ = changed // always true when version != since; empty array is the "no change" signal to the SDK
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

	view, _, err := a.captureManager.CreateMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), &in)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// handleUpdateMockRule handles PUT /api/v1/devices/{app}/{did}/mock-rules/{ruleId}.
// Edits the canned response and note (match key Method+Path is immutable, M5);
// optionally flips the enabled switch. Enabling against another enabled rule on
// the same interface returns 409. note must be non-blank.
func (a *API) handleUpdateMockRule(w http.ResponseWriter, r *http.Request) {
	var in capture.UpdateMockRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if in.Response.StatusCode <= 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "response.statusCode is required and must be positive")
		return
	}
	if strings.TrimSpace(in.Note) == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "note is required and must be non-empty")
		return
	}

	view, _, err := a.captureManager.UpdateMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId"), &in)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDeleteMockRule handles DELETE /api/v1/devices/{app}/{did}/mock-rules/{ruleId}.
func (a *API) handleDeleteMockRule(w http.ResponseWriter, r *http.Request) {
	if _, err := a.captureManager.DeleteMockRule(r.Context(), r.PathValue("app"), r.PathValue("did"), r.PathValue("ruleId")); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateMockRuleInput enforces the contract required fields (MockRuleInput:
// method, path, response.statusCode). Returns false and writes a 400 when
// invalid.
func validateMockRuleInput(w http.ResponseWriter, in *capture.MockRuleInput) bool {
	if in.Method == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "method is required")
		return false
	}
	if in.Path == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "path is required")
		return false
	}
	if in.Response.StatusCode <= 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "response.statusCode is required and must be positive")
		return false
	}
	return true
}
