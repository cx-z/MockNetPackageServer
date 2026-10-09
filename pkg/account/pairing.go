// Package account models pairing tokens used for QR server discovery.
//
// A logged-in Web user issues a short-lived pairing token for an app; the QR
// code rendered on the device page carries it. The SDK presents the token in
// POST /devices/register so the server can auto-register the scanned device
// under the issuing user — the QR scan therefore completes "address config +
// device registration + connect" in one step (D1, 扫码即注册).
//
// Security notes:
//   - Tokens are 32 random bytes hex-encoded (same primitive as session
//     tokens) and are reusable within their TTL (D5: one QR can onboard
//     several devices), so validation never consumes them.
//   - The token binds a user and an app; a token minted for one app cannot
//     register a device of another app.
package account

import "time"

// PairingUse records a device that registered using a pairing token
// : the Web polls the token status and closes the
// QR modal automatically once a fresh registration appears.
type PairingUse struct {
	Did          string    `json:"did"`
	RegisteredAt time.Time `json:"registeredAt"`
}

// PairingToken is a server-issued, user-bound, short-lived credential carried
// in a QR code for device auto-registration.
type PairingToken struct {
	Token     string    `json:"token"`
	User      string    `json:"user"`
	App       string    `json:"app"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// PairedDevices is the list of dids that registered with this token
	// . Reusable tokens  accumulate entries; validation never
	// consumes the token, so a token can keep onboarding devices until TTL.
	PairedDevices []PairingUse `json:"pairedDevices,omitempty"`
}

// Valid reports whether the token is still usable at the given time.
func (p *PairingToken) Valid(now time.Time) bool {
	return now.Before(p.ExpiresAt)
}
