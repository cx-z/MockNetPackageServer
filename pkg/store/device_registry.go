package store

import (
	"context"
	"errors"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// File: device_registry.go
// Device lifecycle: manual creation (Web), SDK registration, heartbeat,
// device views, and deletion (pure move from capture_registry.go).
// ============================================================================
// Devices
// ============================================================================

// CreateManualDevice creates a device entry from the Web UI (M7.2.1): it does
// NOT upsert — an existing (App, Did) returns ErrAlreadyExists so the Web can
// report a conflict. The caller (handler) stamps Owner and Name; this is the
// only device-creation path once M7.2.3 stops SDK auto-registration.
func (m *CaptureManager) CreateManualDevice(ctx context.Context, d *capture.Device) (*capture.Device, error) {
	if _, err := m.devices.Get(ctx, d.App, d.Did); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := time.Now()
	d.RegisteredAt = now
	d.LastSeenAt = now
	if err := m.devices.Create(ctx, d); err != nil {
		return nil, err
	}
	out := *d
	return &out, nil
}

// RegisterDevice registers or re-registers a device (idempotent upsert on
// (App, Did)). A re-registration refreshes metadata and marks the device
// active (LastSeenAt = now). Registration is only accepted for a device
// that already exists OR is new; nothing is rejected here — offline devices
// come back online on their next register/heartbeat.
// UpdateDeviceName changes a device's display name (M7.2.2). Returns
// ErrNotFound when the (app, did) does not exist; ownership is checked by the
// admin handler layer before calling this.
func (m *CaptureManager) UpdateDeviceName(ctx context.Context, app, did, name string) (*capture.Device, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		return nil, err
	}
	d.Name = name
	if err := m.devices.Update(ctx, d); err != nil {
		return nil, err
	}
	out := *d
	return &out, nil
}

func (m *CaptureManager) RegisterDevice(ctx context.Context, d *capture.Device) (*capture.Device, error) {
	now := time.Now()
	d.LastSeenAt = now

	existing, err := m.devices.Get(ctx, d.App, d.Did)
	if errors.Is(err, ErrNotFound) {
		d.RegisteredAt = now
		if err := m.devices.Create(ctx, d); err != nil {
			return nil, err
		}
		out := *d
		return &out, nil
	}
	if err != nil {
		return nil, err
	}

	// Upsert: keep the original registration time, refresh metadata.
	existing.OSVersion = d.OSVersion
	existing.SDKVersion = d.SDKVersion
	existing.AppVersion = d.AppVersion
	// v0.9.0: app display name is metadata — refreshed on re-registration
	// (Web falls back to the bundle id when empty).
	existing.AppName = d.AppName
	existing.Platform = d.Platform
	existing.LastSeenAt = now
	if err := m.devices.Update(ctx, existing); err != nil {
		return nil, err
	}
	out := *existing
	return &out, nil
}

// RegisterDeviceWithPairing registers a device via a validated QR pairing
// token (M9, D7 idempotency). This is the auto-registration path the QR scan
// unlocks:
//   - (app, did) unknown → create with owner = the token's user and
//     name = deviceName (or a platform+did fallback).
//   - (app, did) known → reuse the existing record: owner is kept (filled with
//     the token's user only when empty), name is never overwritten, metadata
//     is refreshed. No duplicate is ever created.
//
// The caller (handler) validates the token before calling; owner is the
// token's User.
func (m *CaptureManager) RegisterDeviceWithPairing(ctx context.Context, d *capture.Device, owner string) (*capture.Device, error) {
	now := time.Now()
	d.LastSeenAt = now

	existing, err := m.devices.Get(ctx, d.App, d.Did)
	if errors.Is(err, ErrNotFound) {
		d.RegisteredAt = now
		d.Owner = owner
		if d.Name == "" {
			d.Name = defaultDeviceName(d)
		}
		if err := m.devices.Create(ctx, d); err != nil {
			return nil, err
		}
		out := *d
		return &out, nil
	}
	if err != nil {
		return nil, err
	}

	// Reuse (D7): never reset owner/name; only fill owner when it was empty.
	if existing.Owner == "" && owner != "" {
		existing.Owner = owner
	}
	existing.OSVersion = d.OSVersion
	existing.SDKVersion = d.SDKVersion
	existing.AppVersion = d.AppVersion
	// v0.9.0: app display name is metadata — refreshed on reuse (D7: name/
	// owner untouched, only metadata + LastSeenAt).
	existing.AppName = d.AppName
	existing.Platform = d.Platform
	existing.LastSeenAt = now
	if err := m.devices.Update(ctx, existing); err != nil {
		return nil, err
	}
	out := *existing
	return &out, nil
}

// defaultDeviceName builds the fallback display name for a device
// auto-registered through the pairing flow when the SDK did not supply one
// (plan: platform + did prefix).
func defaultDeviceName(d *capture.Device) string {
	prefix := string(d.Platform)
	if prefix == "" {
		prefix = "device"
	}
	if len(d.Did) > 12 {
		return prefix + "·" + d.Did[:12]
	}
	return prefix + "·" + d.Did
}

// Heartbeat refreshes the device's last-seen time and returns the device plus
// its active capture session (nil if none). It is the SDK's keep-alive and
// the session-state channel (the SDK starts/stops capture based on the
// returned session).
func (m *CaptureManager) Heartbeat(ctx context.Context, app, did string) (*capture.Device, *capture.CaptureSession, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrDeviceNotRegistered
		}
		return nil, nil, err
	}

	d.LastSeenAt = time.Now()
	if err := m.devices.Update(ctx, d); err != nil {
		return nil, nil, err
	}

	session, err := m.activeSessionFor(ctx, app, did)
	if err != nil {
		return nil, nil, err
	}
	out := *d
	return &out, session, nil
}

// ListDevices returns all devices with derived status and current session.
func (m *CaptureManager) ListDevices(ctx context.Context, filter *DeviceFilter) ([]*capture.DeviceView, error) {
	devices, err := m.devices.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	// One query for all active sessions, grouped by (app, did).
	activeByDevice := make(map[string]*capture.CaptureSession)
	active, err := m.sessions.List(ctx, &SessionFilter{Status: &activeStatus})
	if err != nil {
		return nil, err
	}
	for _, s := range active {
		key := s.App + "\x00" + s.Did
		if _, exists := activeByDevice[key]; !exists {
			activeByDevice[key] = s
		}
	}

	now := time.Now()
	views := make([]*capture.DeviceView, 0, len(devices))
	for _, d := range devices {
		session := activeByDevice[d.App+"\x00"+d.Did]
		var sessionCopy *capture.CaptureSession
		if session != nil {
			sc := *session
			sessionCopy = &sc
		}
		dc := *d
		views = append(views, &capture.DeviceView{
			Device:         &dc,
			Status:         capture.DeriveDeviceStatus(d.LastSeenAt, now, m.cfg.HeartbeatTimeout, session != nil),
			CurrentSession: sessionCopy,
		})
	}
	return views, nil
}

// GetDevice returns a single device with derived status and current session.
func (m *CaptureManager) GetDevice(ctx context.Context, app, did string) (*capture.DeviceView, error) {
	d, err := m.devices.Get(ctx, app, did)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrDeviceNotRegistered
		}
		return nil, err
	}

	session, err := m.activeSessionFor(ctx, app, did)
	if err != nil {
		return nil, err
	}

	var sessionCopy *capture.CaptureSession
	if session != nil {
		sc := *session
		sessionCopy = &sc
	}
	dc := *d
	return &capture.DeviceView{
		Device:         &dc,
		Status:         capture.DeriveDeviceStatus(d.LastSeenAt, time.Now(), m.cfg.HeartbeatTimeout, session != nil),
		CurrentSession: sessionCopy,
	}, nil
}

// DeleteDevice removes a device by (App, Did) and all its associated mock
// rules and active sessions.
func (m *CaptureManager) DeleteDevice(ctx context.Context, app, did string) error {
	// End active sessions for this device (if any)
	if sessions, err := m.sessions.List(ctx, &SessionFilter{App: &app, Did: &did}); err == nil {
		for _, s := range sessions {
			if s.Status == capture.SessionStatusCapturing {
				_ = m.EndSession(ctx, s.ID)
			}
		}
	}
	// Delete associated mock rules
	views, _, _, err := m.ListMockRules(ctx, app, did)
	if err != nil && !errors.Is(err, ErrRuleNotFound) {
		return err
	}
	for _, v := range views {
		_, _ = m.DeleteMockRule(ctx, app, did, v.ID)
	}
	return m.devices.Delete(ctx, app, did)
}
