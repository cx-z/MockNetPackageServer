package capture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDeriveDeviceStatus(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	const timeout = 60 * time.Second

	tests := []struct {
		name            string
		lastSeenAt      time.Time
		now             time.Time
		timeout         time.Duration
		hasActiveSession bool
		want            DeviceStatus
	}{
		{
			name:            "heartbeat timeout wins over active session",
			lastSeenAt:      now.Add(-timeout - time.Second),
			now:             now,
			timeout:         timeout,
			hasActiveSession: true,
			want:            DeviceStatusOffline,
		},
		{
			name:            "active session and fresh heartbeat -> capturing",
			lastSeenAt:      now.Add(-10 * time.Second),
			now:             now,
			timeout:         timeout,
			hasActiveSession: true,
			want:            DeviceStatusCapturing,
		},
		{
			name:            "fresh heartbeat, no session -> idle",
			lastSeenAt:      now.Add(-10 * time.Second),
			now:             now,
			timeout:         timeout,
			hasActiveSession: false,
			want:            DeviceStatusIdle,
		},
		{
			name:            "exactly at timeout boundary is still online",
			lastSeenAt:      now.Add(-timeout),
			now:             now,
			timeout:         timeout,
			hasActiveSession: false,
			want:            DeviceStatusIdle,
		},
		{
			name:            "just past timeout -> offline",
			lastSeenAt:      now.Add(-timeout - time.Nanosecond),
			now:             now,
			timeout:         timeout,
			hasActiveSession: false,
			want:            DeviceStatusOffline,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveDeviceStatus(tt.lastSeenAt, tt.now, tt.timeout, tt.hasActiveSession)
			if got != tt.want {
				t.Errorf("DeriveDeviceStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaptureSession_Start(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	s := &CaptureSession{ID: "s1", App: "app", Did: "d1"}
	s.Start(now)
	if s.Status != SessionStatusCapturing {
		t.Fatalf("Status = %q, want %q", s.Status, SessionStatusCapturing)
	}
	if !s.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, now)
	}

	// Start on an already capturing session keeps StartedAt unchanged.
	later := now.Add(time.Minute)
	s.Start(later)
	if !s.StartedAt.Equal(now) {
		t.Errorf("Start() not idempotent: StartedAt = %v, want %v", s.StartedAt, now)
	}

	// Start on an ended session is a no-op.
	s.End(later)
	if s.Status != SessionStatusEnded {
		t.Fatalf("Status = %q, want %q after End", s.Status, SessionStatusEnded)
	}
	s.Start(later.Add(time.Minute))
	if s.Status != SessionStatusEnded {
		t.Errorf("Start() on ended session changed status to %q", s.Status)
	}
}

func TestCaptureSession_End(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	s := &CaptureSession{ID: "s1", Status: SessionStatusCapturing}
	s.End(now)
	if s.Status != SessionStatusEnded {
		t.Fatalf("Status = %q, want %q", s.Status, SessionStatusEnded)
	}
	if s.EndedAt == nil || !s.EndedAt.Equal(now) {
		t.Errorf("EndedAt = %v, want %v", s.EndedAt, now)
	}

	// Ending an already-ended session keeps the original EndedAt.
	later := now.Add(time.Minute)
	s.End(later)
	if s.EndedAt == nil || !s.EndedAt.Equal(now) {
		t.Errorf("End() not idempotent: EndedAt = %v, want %v", s.EndedAt, now)
	}
}

func TestTrafficEntry_JSONUsesRFC3339(t *testing.T) {
	// The JSON encoding of time.Time must be RFC3339-compatible so the API
	// contract (RFC3339 timestamps) holds end to end.
	ts := time.Date(2026, 9, 18, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	e := TrafficEntry{Timestamp: ts, Method: "GET", URL: "https://example.com/x", DurationMs: 12}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"2026-09-18T09:00:00+08:00"`
	if !strings.Contains(string(b), want) {
		t.Errorf("timestamp not RFC3339: %s (want substring %s)", b, want)
	}
}
