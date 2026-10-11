package cli

import (
	"fmt"
	"testing"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// formatDurationMs (stream_recordings.go)
// ---------------------------------------------------------------------------

func TestFormatDurationMs(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{name: "zero", ms: 0, want: "0ms"},
		{name: "sub-second", ms: 500, want: "500ms"},
		{name: "exactly_one_second", ms: 1000, want: "1.0s"},
		{name: "fractional_seconds", ms: 1500, want: "1.5s"},
		{name: "just_under_minute", ms: 59999, want: "60.0s"},
		{name: "exactly_one_minute", ms: 60000, want: "1m0s"},
		{name: "ninety_seconds", ms: 90000, want: "1m30s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationMs(tt.ms)
			if got != tt.want {
				t.Errorf("formatDurationMs(%d) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatBytes (stream_recordings.go)
// ---------------------------------------------------------------------------

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "small", bytes: 500, want: "500 B"},
		{name: "one_kb", bytes: 1024, want: "1.0 KB"},
		{name: "fractional_kb", bytes: 1536, want: "1.5 KB"},
		{name: "one_mb", bytes: 1048576, want: "1.0 MB"},
		{name: "large_mb", bytes: 5242880, want: "5.0 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// truncatePath (stream_recordings.go)
// ---------------------------------------------------------------------------

func TestTruncatePath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{name: "short_enough", path: "/api/v1", maxLen: 20, want: "/api/v1"},
		{name: "exact_length", path: "/api", maxLen: 4, want: "/api"},
		{name: "truncated", path: "/very/long/api/path/here", maxLen: 15, want: "...pi/path/here"},
		{name: "minimal_max", path: "/a/b/c/d/e", maxLen: 5, want: ".../e"},
		{name: "empty_path", path: "", maxLen: 10, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatePath(tt.path, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncatePath(%q, %d) = %q, want %q", tt.path, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatConnectionError (client.go)
// ---------------------------------------------------------------------------

func TestFormatConnectionError(t *testing.T) {
	t.Run("api_connection_error", func(t *testing.T) {
		err := &APIError{
			StatusCode: 0,
			ErrorCode:  "connection_error",
			Message:    "cannot connect to admin API at http://localhost:4290",
		}
		got := FormatConnectionError(err)
		if got == "" {
			t.Fatal("FormatConnectionError returned empty string")
		}
		// Should contain the original message and suggestions
		if !containsSubstring(got, "cannot connect") {
			t.Errorf("expected 'cannot connect' in output, got %q", got)
		}
		if !containsSubstring(got, "mockd start") {
			t.Errorf("expected 'mockd start' suggestion in output, got %q", got)
		}
	})

	t.Run("non_api_error", func(t *testing.T) {
		err := fmt.Errorf("some random error")
		got := FormatConnectionError(err)
		if got != "some random error" {
			t.Errorf("FormatConnectionError(regular) = %q, want %q", got, "some random error")
		}
	})

	t.Run("api_error_non_connection", func(t *testing.T) {
		err := &APIError{
			StatusCode: 404,
			ErrorCode:  "not_found",
			Message:    "mock not found: abc",
		}
		got := FormatConnectionError(err)
		// Should fall through to err.Error() since it's not connection_error
		if got != "mock not found: abc" {
			t.Errorf("FormatConnectionError(not_found) = %q, want %q", got, "mock not found: abc")
		}
	})
}

// ---------------------------------------------------------------------------
// FormatNotFoundError (client.go)
// ---------------------------------------------------------------------------

func TestFormatNotFoundError(t *testing.T) {
	tests := []struct {
		name         string
		resourceType string
		id           string
		wantContains []string
	}{
		{
			name:         "mock_not_found",
			resourceType: "mock",
			id:           "http_abc123",
			wantContains: []string{"mock", "http_abc123", "not found", "mockd list"},
		},
		{
			name:         "workspace_not_found",
			resourceType: "workspace",
			id:           "ws-001",
			wantContains: []string{"workspace", "ws-001", "not found"},
		},
		{
			name:         "empty_id",
			resourceType: "resource",
			id:           "",
			wantContains: []string{"resource", "not found"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatNotFoundError(tt.resourceType, tt.id)
			for _, want := range tt.wantContains {
				if !containsSubstring(got, want) {
					t.Errorf("FormatNotFoundError(%q, %q) missing %q, got %q",
						tt.resourceType, tt.id, want, got)
				}
			}
		})
	}
}

func TestMessageTypeString(t *testing.T) {
	tests := []struct {
		name    string
		msgType int
		want    string
	}{
		{name: "text", msgType: websocket.TextMessage, want: "text"},
		{name: "binary", msgType: websocket.BinaryMessage, want: "binary"},
		{name: "close", msgType: websocket.CloseMessage, want: "close"},
		{name: "ping", msgType: websocket.PingMessage, want: "ping"},
		{name: "pong", msgType: websocket.PongMessage, want: "pong"},
		{name: "unknown", msgType: 99, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messageTypeString(tt.msgType)
			if got != tt.want {
				t.Errorf("messageTypeString(%d) = %q, want %q", tt.msgType, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractTypeName (grpc.go)
// ---------------------------------------------------------------------------

func TestExtractTypeName(t *testing.T) {
	tests := []struct {
		name string
		fqn  string
		want string
	}{
		{name: "fully_qualified", fqn: ".google.protobuf.Empty", want: "Empty"},
		{name: "package_prefixed", fqn: "greet.HelloRequest", want: "HelloRequest"},
		{name: "simple_name", fqn: "MyType", want: "MyType"},
		{name: "deep_nesting", fqn: "a.b.c.d.Response", want: "Response"},
		{name: "empty", fqn: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTypeName(tt.fqn)
			if got != tt.want {
				t.Errorf("extractTypeName(%q) = %q, want %q", tt.fqn, got, tt.want)
			}
		})
	}
}
