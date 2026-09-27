// MockNetPack device API handlers (pure move from capture_handlers.go):
// manual registration, SDK register/heartbeat with dynamic heartbeat config,
// device list/get/rename/delete.
package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// handleCreateDevice handles POST /api/v1/devices — Web manual device
// registration. The logged-in user becomes the owner; an existing (App, Did)
// conflicts. SDK auto-registration is removed in M7.2.3, making this the only
// creation path afterwards.
func (a *API) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req CreateDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.App = strings.TrimSpace(req.App)
	req.Did = strings.TrimSpace(req.Did)
	req.Name = strings.TrimSpace(req.Name)
	if req.App == "" || req.Did == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "app, did and name are required")
		return
	}
	if !allowedApps[req.App] {
		writeError(w, http.StatusBadRequest, "invalid_app", "app is not in the allowed catalog")
		return
	}
	if len(req.Did) > 128 || len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_field", "did must be <=128 chars, name <=64 chars")
		return
	}
	owner := ""
	if u := currentUser(r); u != nil {
		owner = u.Username
	}
	d := &capture.Device{
		App:      req.App,
		Did:      req.Did,
		Name:     req.Name,
		Owner:    owner,
		Platform: capture.PlatformIOS,
	}
	if _, err := a.captureManager.CreateManualDevice(r.Context(), d); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "device_taken", "device already registered")
			return
		}
		writeCaptureError(w, err)
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// handleRegisterDevice handles POST /api/v1/devices/register.
func (a *API) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.App = strings.TrimSpace(req.App)
	req.Did = strings.TrimSpace(req.Did)
	req.PairingToken = strings.TrimSpace(req.PairingToken)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	req.AppName = strings.TrimSpace(req.AppName)
	req.Platform = capture.Platform(strings.TrimSpace(string(req.Platform)))
	req.OSVersion = strings.TrimSpace(req.OSVersion)
	req.SDKVersion = strings.TrimSpace(req.SDKVersion)
	req.AppVersion = strings.TrimSpace(req.AppVersion)
	if req.App == "" {
		writeError(w, http.StatusBadRequest, "missing_app", "app is required")
		return
	}
	if req.Did == "" {
		writeError(w, http.StatusBadRequest, "missing_did", "did is required")
		return
	}
	// 4.19: every registration field has a length cap — before, platform /
	// osVersion / sdkVersion / appVersion were only bounded by the 10MB body
	// limit, so a misbehaving SDK could persist arbitrary blobs as device
	// metadata. Caps: app/did/pairingToken <=128, deviceName/appName <=64,
	// platform/osVersion <=32, sdkVersion/appVersion <=64.
	if len(req.App) > 128 || len(req.Did) > 128 || len(req.PairingToken) > 128 || len(req.DeviceName) > 64 || len(req.AppName) > 64 ||
		len(req.Platform) > 32 || len(req.OSVersion) > 32 || len(req.SDKVersion) > 64 || len(req.AppVersion) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_field",
			"app/did/pairingToken <=128 chars, deviceName/appName <=64 chars, platform/osVersion <=32 chars, sdkVersion/appVersion <=64 chars")
		return
	}

	d := &capture.Device{
		App:        req.App,
		AppName:    req.AppName,
		Did:        req.Did,
		Name:       req.DeviceName,
		Platform:   req.Platform,
		OSVersion:  req.OSVersion,
		SDKVersion: req.SDKVersion,
		AppVersion: req.AppVersion,
	}

	if req.PairingToken != "" {
		// M9 (v0.8.0): QR pairing flow. A valid token unlocks auto-registration:
		// unknown (app, did) is created under the token's user (D1), and an
		// existing record is reused without duplication (D7).
		tok, err := a.captureManager.ValidatePairingToken(r.Context(), req.PairingToken, req.App)
		if err != nil {
			writeCaptureError(w, err)
			return
		}
		if _, err := a.captureManager.RegisterDeviceWithPairing(r.Context(), d, tok.User); err != nil {
			writeCaptureError(w, err)
			return
		}
		// M9.3-fix (v0.8.2): record the registration on the token so the Web
		// can detect scan completion and auto-close the QR modal. Best-effort.
		a.captureManager.RecordPairingUse(r.Context(), req.PairingToken, req.Did)
	} else {
		// M7.2.3: SDK auto-registration is retired. A device must already exist
		// (created manually in the Web UI). An unknown did gets a technical 404 —
		// no user-facing "please register in Web" copy here; that guidance belongs
		// to the Web UI, not the debug SDK channel.
		if _, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did); err != nil {
			writeCaptureError(w, err)
			return
		}
		if _, err := a.captureManager.RegisterDevice(r.Context(), d); err != nil {
			writeCaptureError(w, err)
			return
		}
	}

	view, err := a.captureManager.GetDevice(r.Context(), req.App, req.Did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RegisterDeviceResponse{
		Device:       view,
		ServerConfig: idleHeartbeatConfig(a.captureManager.ServerConfig()),
	})
}

// idleHeartbeatConfig 返回 idle（未抓包）状态的心跳间隔（M8.2：5s，快速感知会话激活）。
func idleHeartbeatConfig(cfg capture.ServerConfig) capture.ServerConfig {
	cfg.HeartbeatIntervalSeconds = 5
	return cfg
}

// capturingHeartbeatConfig 返回 capturing（抓包中）状态的心跳间隔（M8.3：3s，快速感知规则变更）。
func capturingHeartbeatConfig(cfg capture.ServerConfig) capture.ServerConfig {
	cfg.HeartbeatIntervalSeconds = 3
	return cfg
}

// handleDeviceHeartbeat handles POST /api/v1/devices/{app}/{did}/heartbeat.
func (a *API) handleDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")

	var req CaptureHeartbeatRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}

	_, session, err := a.captureManager.Heartbeat(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	rulesVersion, err := a.captureManager.RuleVersion(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HeartbeatResponse{
		OK:         true,
		ServerTime: time.Now(),
		// M8.2/M8.3：心跳间隔按会话状态动态下发——capturing 3s（快速感知规则变更）、
		// idle 5s（快速感知会话激活）；SDK 按响应间隔调度下一次心跳。
		ServerConfig: heartbeatConfigForSession(a.captureManager.ServerConfig(), session),
		Session:      session,
		RulesVersion: rulesVersion,
	})
}

// heartbeatConfigForSession 根据会话是否激活返回对应心跳间隔配置。
func heartbeatConfigForSession(base capture.ServerConfig, session *capture.CaptureSession) capture.ServerConfig {
	if session != nil {
		return capturingHeartbeatConfig(base)
	}
	return idleHeartbeatConfig(base)
}

// handleListDevices handles GET /api/v1/devices (Web device list).
func (a *API) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := a.captureManager.ListDevices(r.Context(), nil)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	// M7.2.2 ownership filtering: admin sees all; a dev sees only its own devices.
	if u := currentUser(r); u != nil && !isAdmin(u) {
		filtered := make([]*capture.DeviceView, 0, len(devices))
		for _, d := range devices {
			if d.Owner == u.Username {
				filtered = append(filtered, d)
			}
		}
		devices = filtered
	}
	writeJSON(w, http.StatusOK, DeviceListResponse{Devices: devices, Total: len(devices)})
}

// handleGetDevice handles GET /api/v1/devices/{app}/{did}.
func (a *API) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	app, did := r.PathValue("app"), r.PathValue("did")
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUpdateDeviceName handles PUT /api/v1/devices/{app}/{did} — rename a
// device. Ownership is enforced (owner or admin only; others see 404).
func (a *API) handleUpdateDeviceName(w http.ResponseWriter, r *http.Request) {
	app, did := r.PathValue("app"), r.PathValue("did")
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	var req UpdateDeviceNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "name is required")
		return
	}
	if len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_field", "name must be <=64 characters")
		return
	}
	if _, err := a.captureManager.UpdateDeviceName(r.Context(), app, did, req.Name); err != nil {
		writeCaptureError(w, err)
		return
	}
	view, err := a.captureManager.GetDevice(r.Context(), app, did)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDeleteDevice handles DELETE /api/v1/devices/{app}/{did}.
// Removes the device and all its mock rules. Requires auth + ownership.
func (a *API) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	did := r.PathValue("did")
	if !a.authorizeDeviceAccess(w, r, app, did) {
		return
	}
	if err := a.captureManager.DeleteDevice(r.Context(), app, did); err != nil {
		writeCaptureError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
