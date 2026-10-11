package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/cli/internal/flags"
	"github.com/getmockd/mockd/pkg/mock"
)

func TestBuildHTTPMock(t *testing.T) {
	tests := []struct {
		name         string
		mockName     string
		path         string
		method       string
		status       int
		body         string
		bodyFile     string
		priority     int
		delay        int
		headers      flags.StringSlice
		matchHeaders flags.StringSlice
		matchQueries flags.StringSlice
		expectError  bool
		errorContain string
		validate     func(*testing.T, *mock.HTTPSpec)
	}{
		{
			name:        "path required",
			path:        "",
			expectError: true,
		},
		{
			name:   "basic GET mock",
			path:   "/api/users",
			method: "GET",
			status: 200,
			body:   `{"users": []}`,
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Matcher.Method != "GET" {
					t.Errorf("method: got %s, want GET", spec.Matcher.Method)
				}
				if spec.Matcher.Path != "/api/users" {
					t.Errorf("path: got %s, want /api/users", spec.Matcher.Path)
				}
				if spec.Response.StatusCode != 200 {
					t.Errorf("status: got %d, want 200", spec.Response.StatusCode)
				}
				if spec.Response.Body != `{"users": []}` {
					t.Errorf("body: got %s, want {\"users\": []}", spec.Response.Body)
				}
			},
		},
		{
			name:   "POST with custom status",
			path:   "/api/users",
			method: "post", // lowercase should be normalized
			status: 201,
			body:   `{"id": "123"}`,
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Matcher.Method != "POST" {
					t.Errorf("method should be uppercase: got %s", spec.Matcher.Method)
				}
				if spec.Response.StatusCode != 201 {
					t.Errorf("status: got %d, want 201", spec.Response.StatusCode)
				}
			},
		},
		{
			name:     "with priority and delay",
			path:     "/api/important",
			method:   "GET",
			status:   200,
			priority: 10,
			delay:    500,
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Priority != 10 {
					t.Errorf("priority: got %d, want 10", spec.Priority)
				}
				if spec.Response.DelayMs != 500 {
					t.Errorf("delay: got %d, want 500", spec.Response.DelayMs)
				}
			},
		},
		{
			name:    "with response headers",
			path:    "/api/data",
			method:  "GET",
			status:  200,
			headers: flags.StringSlice{"Content-Type:application/json", "X-Custom:value"},
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Response.Headers["Content-Type"] != "application/json" {
					t.Errorf("Content-Type header: got %s", spec.Response.Headers["Content-Type"])
				}
				if spec.Response.Headers["X-Custom"] != "value" {
					t.Errorf("X-Custom header: got %s", spec.Response.Headers["X-Custom"])
				}
			},
		},
		{
			name:         "with match headers",
			path:         "/api/auth",
			method:       "GET",
			status:       200,
			matchHeaders: flags.StringSlice{"Authorization:Bearer token"},
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Matcher.Headers["Authorization"] != "Bearer token" {
					t.Errorf("match header: got %s", spec.Matcher.Headers["Authorization"])
				}
			},
		},
		{
			name:         "with match query params",
			path:         "/api/search",
			method:       "GET",
			status:       200,
			matchQueries: flags.StringSlice{"q:search term", "page:1"},
			validate: func(t *testing.T, spec *mock.HTTPSpec) {
				if spec.Matcher.QueryParams["q"] != "search term" {
					t.Errorf("query param q: got %s", spec.Matcher.QueryParams["q"])
				}
				if spec.Matcher.QueryParams["page"] != "1" {
					t.Errorf("query param page: got %s", spec.Matcher.QueryParams["page"])
				}
			},
		},
		{
			name:         "invalid header format",
			path:         "/api/test",
			headers:      flags.StringSlice{"invalid-header-no-colon"},
			expectError:  true,
			errorContain: "invalid header format",
		},
		{
			name:         "invalid match header format",
			path:         "/api/test",
			matchHeaders: flags.StringSlice{"invalid"},
			expectError:  true,
			errorContain: "invalid match-header format",
		},
		{
			name:         "invalid match query format",
			path:         "/api/test",
			matchQueries: flags.StringSlice{"invalid"},
			expectError:  true,
			errorContain: "invalid match-query format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := buildHTTPMock(
				tt.mockName,
				tt.path,
				tt.method,
				tt.status,
				tt.body,
				tt.bodyFile,
				tt.priority,
				tt.delay,
				tt.headers,
				tt.matchHeaders,
				tt.matchQueries,
				false, nil, 100, "", 1, 0, // SSE defaults: disabled
				"", "", // bodyContains, pathPattern defaults
				"",         // statefulOperation default
				"", "", "", // table, bindAction, operation defaults
			)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if tt.errorContain != "" && err != nil {
					if !containsString(err.Error(), tt.errorContain) {
						t.Errorf("error should contain %q, got: %v", tt.errorContain, err)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.Type != mock.TypeHTTP {
				t.Errorf("type: got %s, want http", cfg.Type)
			}

			if tt.validate != nil {
				tt.validate(t, cfg.HTTP)
			}
		})
	}
}

func TestBuildHTTPMock_BodyFile(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFilePath := filepath.Join(tmpDir, "body.json")
	bodyContent := `{"message": "from file"}`

	if err := os.WriteFile(bodyFilePath, []byte(bodyContent), 0644); err != nil {
		t.Fatalf("failed to write body file: %v", err)
	}

	cfg, err := buildHTTPMock(
		"test",
		"/api/test",
		"GET",
		200,
		"",           // empty body
		bodyFilePath, // body from file
		0, 0,
		nil, nil, nil,
		false, nil, 100, "", 1, 0, // SSE defaults
		"", "", // bodyContains, pathPattern defaults
		"",         // statefulOperation default
		"", "", "", // table, bindAction, operation defaults
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTP.Response.Body != bodyContent {
		t.Errorf("body: got %s, want %s", cfg.HTTP.Response.Body, bodyContent)
	}
}

func TestBuildHTTPMock_BodyFileNotFound(t *testing.T) {
	_, err := buildHTTPMock(
		"test",
		"/api/test",
		"GET",
		200,
		"",
		"/nonexistent/body.json",
		0, 0,
		nil, nil, nil,
		false, nil, 100, "", 1, 0, // SSE defaults
		"", "", // bodyContains, pathPattern defaults
		"",         // statefulOperation default
		"", "", "", // table, bindAction, operation defaults
	)

	if err == nil {
		t.Error("expected error for nonexistent body file")
	}
}

func TestBuildWebSocketMock(t *testing.T) {
	tests := []struct {
		name        string
		mockName    string
		path        string
		message     string
		echo        bool
		expectError bool
		validate    func(*testing.T, *mock.WebSocketSpec)
	}{
		{
			name:        "path required",
			path:        "",
			expectError: true,
		},
		{
			name: "basic websocket mock",
			path: "/ws/chat",
			validate: func(t *testing.T, spec *mock.WebSocketSpec) {
				if spec.Path != "/ws/chat" {
					t.Errorf("path: got %s, want /ws/chat", spec.Path)
				}
			},
		},
		{
			name: "with echo mode",
			path: "/ws/echo",
			echo: true,
			validate: func(t *testing.T, spec *mock.WebSocketSpec) {
				if spec.EchoMode == nil || !*spec.EchoMode {
					t.Error("echo mode should be enabled")
				}
			},
		},
		{
			name:    "with default response",
			path:    "/ws/notify",
			message: `{"type": "connected"}`,
			validate: func(t *testing.T, spec *mock.WebSocketSpec) {
				if spec.DefaultResponse == nil {
					t.Fatal("default response should be set")
				}
				if spec.DefaultResponse.Type != "json" {
					t.Errorf("response type: got %s, want json", spec.DefaultResponse.Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := buildWebSocketMock(tt.mockName, tt.path, tt.message, tt.echo)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.Type != mock.TypeWebSocket {
				t.Errorf("type: got %s, want websocket", cfg.Type)
			}

			if tt.validate != nil {
				tt.validate(t, cfg.WebSocket)
			}
		})
	}
}

func TestBuildGRPCMock(t *testing.T) {
	// Create a temporary proto file for tests that need one
	tmpDir := t.TempDir()
	protoPath := filepath.Join(tmpDir, "test.proto")
	if err := os.WriteFile(protoPath, []byte(`syntax = "proto3"; package test;`), 0644); err != nil {
		t.Fatalf("failed to create temp proto file: %v", err)
	}
	dummyProto := flags.StringSlice{protoPath}
	emptyProto := flags.StringSlice{}
	emptyPaths := flags.StringSlice{}

	tests := []struct {
		name        string
		mockName    string
		service     string
		rpcMethod   string
		response    string
		protoFiles  flags.StringSlice
		expectError bool
		validate    func(*testing.T, *mock.GRPCSpec)
	}{
		{
			name:        "proto file required",
			service:     "greeter.Greeter",
			rpcMethod:   "SayHello",
			protoFiles:  emptyProto,
			expectError: true,
		},
		{
			name:        "service required",
			service:     "",
			rpcMethod:   "SayHello",
			protoFiles:  dummyProto,
			expectError: true,
		},
		{
			name:        "rpc method required",
			service:     "greeter.Greeter",
			rpcMethod:   "",
			protoFiles:  dummyProto,
			expectError: true,
		},
		{
			name:       "basic grpc mock",
			service:    "greeter.Greeter",
			rpcMethod:  "SayHello",
			protoFiles: dummyProto,
			validate: func(t *testing.T, spec *mock.GRPCSpec) {
				if _, ok := spec.Services["greeter.Greeter"]; !ok {
					t.Error("service greeter.Greeter should exist")
				}
				if _, ok := spec.Services["greeter.Greeter"].Methods["SayHello"]; !ok {
					t.Error("method SayHello should exist")
				}
				// Single proto file is set in ProtoFile (singular), not ProtoFiles
				if spec.ProtoFile == "" && len(spec.ProtoFiles) == 0 {
					t.Error("proto file should be set")
				}
			},
		},
		{
			name:       "with response",
			service:    "greeter.Greeter",
			rpcMethod:  "SayHello",
			response:   `{"message": "Hello!"}`,
			protoFiles: dummyProto,
			validate: func(t *testing.T, spec *mock.GRPCSpec) {
				methodConfig := spec.Services["greeter.Greeter"].Methods["SayHello"]
				if methodConfig.Response == nil {
					t.Error("method should have response")
				}
			},
		},
		{
			name:        "invalid JSON response",
			service:     "greeter.Greeter",
			rpcMethod:   "SayHello",
			response:    `{invalid}`,
			protoFiles:  dummyProto,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := buildGRPCMock(tt.mockName, tt.service, tt.rpcMethod, tt.response, 50051, tt.protoFiles, emptyPaths)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.Type != mock.TypeGRPC {
				t.Errorf("type: got %s, want grpc", cfg.Type)
			}

			if tt.validate != nil {
				tt.validate(t, cfg.GRPC)
			}
		})
	}
}

func TestOutputJSONResult(t *testing.T) {
	// Test JSON output formatting
	// We can't easily capture stdout in unit tests, but we can verify
	// the JSON encoding doesn't panic

	t.Run("http mock json output", func(t *testing.T) {
		cfg, _ := buildHTTPMock("test", "/api/test", "GET", 200, "{}", "", 0, 0, nil, nil, nil, false, nil, 100, "", 1, 0, "", "", "", "", "", "")
		cfg.ID = "test-id"

		createResult := &CreateMockResult{
			Mock:   cfg,
			Action: "created",
		}

		// Capture stdout
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := outputJSONResult(createResult, mock.TypeHTTP)

		w.Close()
		os.Stdout = oldStdout

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Read and validate output
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		output := string(buf[:n])

		var jsonResult map[string]interface{}
		if err := json.Unmarshal([]byte(output), &jsonResult); err != nil {
			t.Fatalf("invalid JSON output: %v\nOutput: %s", err, output)
		}

		if jsonResult["id"] != "test-id" {
			t.Errorf("id: got %v, want test-id", jsonResult["id"])
		}
		if jsonResult["type"] != "http" {
			t.Errorf("type: got %v, want http", jsonResult["type"])
		}
	})
}

func TestBuildHTTPMock_StatefulOperation(t *testing.T) {
	t.Run("sets StatefulOperation on HTTPSpec", func(t *testing.T) {
		cfg, err := buildHTTPMock(
			"transfer", "/api/transfer", "POST", 200, "", "", 0, 0,
			nil, nil, nil,
			false, nil, 100, "", 1, 0,
			"", "",
			"TransferFunds",
			"", "", "", // table, bindAction, operation defaults
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.HTTP.StatefulOperation != "TransferFunds" {
			t.Errorf("statefulOperation: got %q, want TransferFunds", cfg.HTTP.StatefulOperation)
		}
		if cfg.HTTP.Response != nil {
			t.Error("response should be nil when statefulOperation is set")
		}
		if cfg.HTTP.SSE != nil {
			t.Error("sse should be nil when statefulOperation is set")
		}
	})

	t.Run("mutually exclusive with --body", func(t *testing.T) {
		_, err := buildHTTPMock(
			"test", "/api/test", "POST", 200,
			`{"data":"value"}`, "", // body set
			0, 0, nil, nil, nil,
			false, nil, 100, "", 1, 0,
			"", "",
			"SomeOperation",
			"", "", "", // table, bindAction, operation defaults
		)
		if err == nil {
			t.Error("expected error when both --body and --stateful-operation are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error should mention mutual exclusivity, got: %v", err)
		}
	})

	t.Run("mutually exclusive with --body-file", func(t *testing.T) {
		_, err := buildHTTPMock(
			"test", "/api/test", "POST", 200,
			"", "/some/file.json", // bodyFile set
			0, 0, nil, nil, nil,
			false, nil, 100, "", 1, 0,
			"", "",
			"SomeOperation",
			"", "", "", // table, bindAction, operation defaults
		)
		if err == nil {
			t.Error("expected error when both --body-file and --stateful-operation are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error should mention mutual exclusivity, got: %v", err)
		}
	})

	t.Run("mutually exclusive with --sse", func(t *testing.T) {
		_, err := buildHTTPMock(
			"test", "/api/test", "GET", 200, "", "", 0, 0,
			nil, nil, nil,
			true, nil, 100, "", 1, 0, // sse=true
			"", "",
			"SomeOperation",
			"", "", "", // table, bindAction, operation defaults
		)
		if err == nil {
			t.Error("expected error when both --sse and --stateful-operation are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error should mention mutual exclusivity, got: %v", err)
		}
	})

	t.Run("empty statefulOperation falls through to normal response", func(t *testing.T) {
		cfg, err := buildHTTPMock(
			"test", "/api/test", "GET", 200, `{"ok":true}`, "", 0, 0,
			nil, nil, nil,
			false, nil, 100, "", 1, 0,
			"", "",
			"",         // empty — normal mock
			"", "", "", // table, bindAction, operation defaults
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.HTTP.StatefulOperation != "" {
			t.Errorf("statefulOperation should be empty, got %q", cfg.HTTP.StatefulOperation)
		}
		if cfg.HTTP.Response == nil {
			t.Error("response should be set for normal mocks")
		}
	})

	t.Run("JSON output includes statefulOperation", func(t *testing.T) {
		cfg, _ := buildHTTPMock(
			"transfer", "/api/transfer", "POST", 200, "", "", 0, 0,
			nil, nil, nil,
			false, nil, 100, "", 1, 0,
			"", "",
			"TransferFunds",
			"", "", "", // table, bindAction, operation defaults
		)
		cfg.ID = "test-op-id"

		createResult := &CreateMockResult{
			Mock:   cfg,
			Action: "created",
		}

		// Capture stdout
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := outputJSONResult(createResult, mock.TypeHTTP)

		w.Close()
		os.Stdout = oldStdout

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		output := string(buf[:n])

		var jsonResult map[string]interface{}
		if err := json.Unmarshal([]byte(output), &jsonResult); err != nil {
			t.Fatalf("invalid JSON output: %v\nOutput: %s", err, output)
		}

		if jsonResult["statefulOperation"] != "TransferFunds" {
			t.Errorf("statefulOperation: got %v, want TransferFunds", jsonResult["statefulOperation"])
		}
		// statusCode should be omitted (zero value with omitempty)
		if sc, ok := jsonResult["statusCode"]; ok && sc != nil && sc != float64(0) {
			t.Errorf("statusCode should be omitted or 0 when statefulOperation is set, got %v", sc)
		}
	})
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
