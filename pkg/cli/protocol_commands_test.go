package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// wsStatusCmd — WebSocket status via admin API
// =============================================================================

func TestWSStatus_NoMocks(t *testing.T) {
	// Cannot use t.Parallel() — modifies globals: adminURL, jsonOutput, os.Stdout

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/mocks" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"mocks": []interface{}{},
				"count": 0,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	oldAdminURL := adminURL
	oldJSON := jsonOutput
	adminURL = ts.URL
	jsonOutput = false
	defer func() {
		adminURL = oldAdminURL
		jsonOutput = oldJSON
	}()

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := wsStatusCmd.RunE(wsStatusCmd, nil)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)

	if err != nil {
		t.Fatalf("wsStatusCmd.RunE() returned error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "no mocks configured") {
		t.Errorf("expected 'no mocks configured' in output, got: %s", output)
	}
}

func TestWSStatus_WithMocks(t *testing.T) {
	// Cannot use t.Parallel() — modifies globals: adminURL, jsonOutput, os.Stdout

	enabled := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/mocks" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"mocks": []map[string]interface{}{
					{
						"id":      "ws_001",
						"type":    "websocket",
						"enabled": enabled,
						"websocket": map[string]interface{}{
							"path": "/ws/chat",
						},
					},
					{
						"id":      "ws_002",
						"type":    "websocket",
						"enabled": enabled,
						"websocket": map[string]interface{}{
							"path": "/ws/events",
						},
					},
					{
						"id":   "http_001",
						"type": "http",
						"http": map[string]interface{}{
							"matcher":  map[string]interface{}{"method": "GET", "path": "/api"},
							"response": map[string]interface{}{"statusCode": 200},
						},
					},
				},
				"count": 3,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	oldAdminURL := adminURL
	oldJSON := jsonOutput
	adminURL = ts.URL
	jsonOutput = false
	defer func() {
		adminURL = oldAdminURL
		jsonOutput = oldJSON
	}()

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := wsStatusCmd.RunE(wsStatusCmd, nil)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)

	if err != nil {
		t.Fatalf("wsStatusCmd.RunE() returned error: %v", err)
	}

	output := buf.String()
	// Should report 2 websocket mocks (not 3, HTTP one is filtered out)
	if !strings.Contains(output, "2 mock(s) configured") {
		t.Errorf("expected '2 mock(s) configured' in output, got: %s", output)
	}
	if !strings.Contains(output, "/ws/chat") {
		t.Errorf("expected '/ws/chat' in output, got: %s", output)
	}
	if !strings.Contains(output, "/ws/events") {
		t.Errorf("expected '/ws/events' in output, got: %s", output)
	}
	if !strings.Contains(output, "ws_001") {
		t.Errorf("expected 'ws_001' in output, got: %s", output)
	}
}

func TestWSStatus_ServerError(t *testing.T) {
	// Cannot use t.Parallel() — modifies globals: adminURL, jsonOutput

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "internal_error",
			"message": "engine unavailable",
		})
	}))
	defer ts.Close()

	oldAdminURL := adminURL
	oldJSON := jsonOutput
	adminURL = ts.URL
	jsonOutput = false
	defer func() {
		adminURL = oldAdminURL
		jsonOutput = oldJSON
	}()

	err := wsStatusCmd.RunE(wsStatusCmd, nil)
	if err == nil {
		t.Fatal("wsStatusCmd.RunE() should return error for 500 response")
	}
	if !strings.Contains(err.Error(), "failed to get WebSocket status") {
		t.Errorf("error message should mention WebSocket status, got: %v", err)
	}
}

// =============================================================================

func TestPrintGRPCCallInstructions(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      string
		serviceMethod string
		body          string
		metadata      string
		plaintext     bool
		wantContains  []string
	}{
		{
			name:          "basic_plaintext",
			endpoint:      "localhost:50051",
			serviceMethod: "greet.Greeter/SayHello",
			body:          `{"name":"World"}`,
			plaintext:     true,
			wantContains: []string{
				"grpcurl is not installed",
				"brew install grpcurl",
				"go install github.com/fullstorydev/grpcurl",
				"grpcurl -plaintext",
				"-d '{\"name\":\"World\"}'",
				"localhost:50051",
				"greet.Greeter/SayHello",
			},
		},
		{
			name:          "no_plaintext",
			endpoint:      "api.example.com:443",
			serviceMethod: "api.Service/Method",
			body:          `{}`,
			plaintext:     false,
			wantContains: []string{
				"api.example.com:443",
				"api.Service/Method",
			},
		},
		{
			name:          "with_metadata",
			endpoint:      "localhost:50051",
			serviceMethod: "svc/Method",
			body:          `{"id":1}`,
			metadata:      "authorization:Bearer tok123,x-request-id:abc",
			plaintext:     true,
			wantContains: []string{
				"-H 'authorization: Bearer tok123'",
				"-H 'x-request-id: abc'",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			err := printGRPCCallInstructions(tt.endpoint, tt.serviceMethod, tt.body, tt.metadata, tt.plaintext)

			w.Close()
			os.Stdout = oldStdout

			var buf bytes.Buffer
			buf.ReadFrom(r)
			output := buf.String()

			// Should always return an error (grpcurl not found)
			if err == nil {
				t.Fatal("printGRPCCallInstructions() should return error")
			}
			if err.Error() != "grpcurl not found" {
				t.Errorf("error = %q, want 'grpcurl not found'", err.Error())
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(output, want) {
					t.Errorf("output missing %q, got:\n%s", want, output)
				}
			}

			// No plaintext flag when plaintext=false
			if !tt.plaintext && strings.Contains(output, "-plaintext") {
				t.Errorf("plaintext=false should not produce -plaintext flag, got:\n%s", output)
			}
		})
	}
}
