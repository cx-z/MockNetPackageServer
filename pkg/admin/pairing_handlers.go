// MockNetPack QR pairing token API (M9, contract v0.8.0): a logged-in Web
// user issues a short-lived pairing token for an app; the SDK presents it in
// POST /devices/register to auto-register the scanned device under that user.

package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// CreatePairingTokenRequest is the POST /pairing-tokens body.
type CreatePairingTokenRequest struct {
	App string `json:"app"`
}

// CreatePairingTokenResponse is the POST /pairing-tokens reply.
type CreatePairingTokenResponse struct {
	Token     string    `json:"token"`
	App       string    `json:"app"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// handleCreatePairingToken handles POST /api/v1/pairing-tokens (requireAuth):
// issue a 10-minute pairing token for the given app on behalf of the logged-in
// user. The same user may issue repeatedly (D5: tokens are reusable until
// expiry, so one QR can onboard several devices).
func (a *API) handleCreatePairingToken(w http.ResponseWriter, r *http.Request) {
	var req CreatePairingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.App = strings.TrimSpace(req.App)
	if req.App == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "app is required")
		return
	}
	if !loadAllowedApps()[req.App] {
		writeError(w, http.StatusBadRequest, "invalid_app", "app is not in the allowed catalog")
		return
	}

	owner := ""
	if u := currentUser(r); u != nil {
		owner = u.Username
	}
	tok, err := a.captureManager.CreatePairingToken(r.Context(), owner, req.App)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreatePairingTokenResponse{
		Token:     tok.Token,
		App:       tok.App,
		ExpiresAt: tok.ExpiresAt,
	})
}

// PairingTokenStatusResponse is the GET /pairing-tokens/{token} reply
// (M9.3-fix, contract v0.8.2): the Web polls it while the QR modal is open
// to detect that a device registered with this token, then auto-closes the
// modal and asks the user to name the device.
type PairingTokenStatusResponse struct {
	Token         string           `json:"token"`
	App           string           `json:"app"`
	ExpiresAt     time.Time        `json:"expiresAt"`
	Used          bool             `json:"used"`
	PairedDevices []PairingUseView `json:"pairedDevices"`
}

// PairingUseView is one device that registered using the token.
type PairingUseView struct {
	Did          string    `json:"did"`
	Name         string    `json:"name,omitempty"`
	RegisteredAt time.Time `json:"registeredAt"`
}

// handleGetPairingToken handles GET /api/v1/pairing-tokens/{token}
// (requireAuth): pairing-token status + the devices that registered with it.
// Not-found tokens get 404; expired tokens are still returned (the Web stops
// polling on its own countdown and only needs the status while the modal is
// open).
func (a *API) handleGetPairingToken(w http.ResponseWriter, r *http.Request) {
	tok, err := a.captureManager.GetPairingToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeCaptureError(w, err) // 404 for unknown tokens
		return
	}

	uses := make([]PairingUseView, 0, len(tok.PairedDevices))
	for _, u := range tok.PairedDevices {
		view := PairingUseView{Did: u.Did, RegisteredAt: u.RegisteredAt}
		if dv, err := a.captureManager.GetDevice(r.Context(), tok.App, u.Did); err == nil {
			view.Name = dv.Name
		}
		uses = append(uses, view)
	}
	writeJSON(w, http.StatusOK, PairingTokenStatusResponse{
		Token:         tok.Token,
		App:           tok.App,
		ExpiresAt:     tok.ExpiresAt,
		Used:          len(uses) > 0,
		PairedDevices: uses,
	})
}
