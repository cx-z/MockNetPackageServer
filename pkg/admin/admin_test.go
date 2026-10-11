package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/admin/engineclient"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/mock"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Regression Tests for Bug 3.6: Localhost Auth Bypass Variants
// Tests that isLocalhost correctly identifies localhost connections
// ============================================================================

func TestIsLocalhost_127_0_0_1_ReturnsTrue(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	assert.True(t, isLocalhost(req), "127.0.0.1 should be identified as localhost")
}

func TestIsLocalhost_IPv6Loopback_ReturnsTrue(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:12345"

	assert.True(t, isLocalhost(req), "::1 (IPv6 loopback) should be identified as localhost")
}

func TestIsLocalhost_127_0_0_2_ReturnsTrue(t *testing.T) {
	// The entire 127.0.0.0/8 range is loopback
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.2:12345"

	assert.True(t, isLocalhost(req), "127.0.0.2 (full loopback range) should be identified as localhost")
}

func TestIsLocalhost_FullLoopbackRange(t *testing.T) {
	// Table-driven test for various loopback addresses
	tests := []struct {
		name       string
		remoteAddr string
		expected   bool
	}{
		{"127.0.0.1 standard", "127.0.0.1:8080", true},
		{"127.0.0.2", "127.0.0.2:8080", true},
		{"127.0.1.1", "127.0.1.1:8080", true},
		{"127.255.255.254", "127.255.255.254:8080", true},
		{"::1 IPv6", "[::1]:8080", true},
		{"127.0.0.1 no port", "127.0.0.1", true},
		{"::1 no port", "::1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			assert.Equal(t, tt.expected, isLocalhost(req))
		})
	}
}

func TestIsLocalhost_ExternalIP_ReturnsFalse(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
	}{
		{"external IPv4", "192.168.1.100:12345"},
		{"public IPv4", "8.8.8.8:12345"},
		{"external IPv6", "[2001:db8::1]:12345"},
		{"private network", "10.0.0.1:12345"},
		{"link-local", "169.254.1.1:12345"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			assert.False(t, isLocalhost(req), "%s should NOT be identified as localhost", tt.remoteAddr)
		})
	}
}

func TestIsLocalhost_EmptyHost_ReturnsFalse(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = ""

	assert.False(t, isLocalhost(req), "empty RemoteAddr should return false")
}

func TestIsLocalhost_InvalidIP_ReturnsFalse(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
	}{
		{"invalid format", "not-an-ip:8080"},
		{"partial IP", "127.0.0:8080"},
		{"letters in IP", "127.a.0.1:8080"},
		{"out of range", "256.0.0.1:8080"},
		{"empty after split", ":8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			assert.False(t, isLocalhost(req), "%s should return false", tt.remoteAddr)
		})
	}
}

// ============================================================================
// Regression Tests for Bug 3.6: Localhost Bypass Behavior
// ============================================================================

func TestLocalhostBypass_Disabled_RequiresAuth(t *testing.T) {
	// Create API with localhost bypass DISABLED
	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithAPIKey("test-api-key"),
		WithAllowLocalhostBypass(false),
		WithAPIKeyAllowLocalhost(false),
	)
	defer api.Stop()

	// Make request from localhost WITHOUT auth header
	// Note: /health is exempt from auth, so we use /status instead
	req := httptest.NewRequest("GET", "/status", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	// Serve through the middleware chain
	api.httpServer.Handler.ServeHTTP(rec, req)

	// Should require authentication (401 Unauthorized)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"localhost requests should require auth when bypass is disabled")
}

func TestLocalhostBypass_Enabled_AllowsLocalhost(t *testing.T) {
	// Create API with localhost bypass ENABLED
	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithAPIKey("test-api-key"),
		WithAPIKeyAllowLocalhost(true),
	)
	defer api.Stop()

	// Make request from localhost WITHOUT auth header
	// Using /status which requires auth (unlike /health which is exempt)
	// Note: /status returns 503 without engine, but that's OK - we just need to verify
	// auth was bypassed (not 401)
	req := httptest.NewRequest("GET", "/status", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	// Serve through the middleware chain
	api.httpServer.Handler.ServeHTTP(rec, req)

	// Should not be 401 (auth bypassed) - may be 503 (no engine) or 200
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
		"localhost requests should bypass auth when enabled")
}

func TestLocalhostBypass_Enabled_ExternalStillRequiresAuth(t *testing.T) {
	// Create API with localhost bypass ENABLED
	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithAPIKey("test-api-key"),
		WithAPIKeyAllowLocalhost(true),
	)
	defer api.Stop()

	// Make request from EXTERNAL IP without auth
	// Using /status which requires auth (unlike /health which is exempt)
	req := httptest.NewRequest("GET", "/status", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()

	api.httpServer.Handler.ServeHTTP(rec, req)

	// External IPs should still require auth
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"external requests should require auth even when localhost bypass is enabled")
}

// ============================================================================
// Regression Tests for Bug 2.8: Bulk Create Doesn't Check Duplicate IDs
// ============================================================================

func TestBulkCreate_DuplicateIDs_ReturnsError(t *testing.T) {
	// Create temp directory for test isolation
	tmpDir := t.TempDir()

	api := NewAPI(0, WithDataDir(tmpDir))
	defer api.Stop()

	// Request body with duplicate IDs
	mocks := []*mock.Mock{
		{ID: "duplicate-id", Name: "Mock 1", Type: mock.TypeHTTP},
		{ID: "unique-id", Name: "Mock 2", Type: mock.TypeHTTP},
		{ID: "duplicate-id", Name: "Mock 3 - Duplicate", Type: mock.TypeHTTP},
	}
	body, _ := json.Marshal(mocks)

	req := httptest.NewRequest("POST", "/mocks/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	api.handleBulkCreateUnifiedMocks(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"bulk create with duplicate IDs should return 400")

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Equal(t, "duplicate_id", resp["error"],
		"error code should be duplicate_id")
	assert.Contains(t, resp["message"].(string), "duplicate-id",
		"message should mention the duplicate ID")
}

func TestBulkCreate_UniqueIDs_Succeeds(t *testing.T) {
	tmpDir := t.TempDir()

	api := NewAPI(0, WithDataDir(tmpDir))
	defer api.Stop()

	// Request body with unique IDs
	mocks := []*mock.Mock{
		{ID: "mock-1", Name: "Mock 1", Type: mock.TypeHTTP},
		{ID: "mock-2", Name: "Mock 2", Type: mock.TypeHTTP},
		{ID: "mock-3", Name: "Mock 3", Type: mock.TypeHTTP},
	}
	body, _ := json.Marshal(mocks)

	req := httptest.NewRequest("POST", "/mocks/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	api.handleBulkCreateUnifiedMocks(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"bulk create with unique IDs should succeed")

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Equal(t, float64(3), resp["created"],
		"should report 3 mocks created")
}

func TestBulkCreate_PortConflictWithinBatch_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()

	api := NewAPI(0, WithDataDir(tmpDir))
	defer api.Stop()

	// Two gRPC mocks using the same port in the same batch
	mocks := []*mock.Mock{
		{
			ID:          "grpc-1",
			Name:        "gRPC Mock 1",
			Type:        mock.TypeGRPC,
			WorkspaceID: store.DefaultWorkspaceID,
			GRPC:        &mock.GRPCSpec{Port: 50051},
		},
		{
			ID:          "grpc-2",
			Name:        "gRPC Mock 2",
			Type:        mock.TypeGRPC,
			WorkspaceID: store.DefaultWorkspaceID,
			GRPC:        &mock.GRPCSpec{Port: 50051}, // Same port - conflict
		},
	}
	body, _ := json.Marshal(mocks)

	req := httptest.NewRequest("POST", "/mocks/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	api.handleBulkCreateUnifiedMocks(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code,
		"bulk create with port conflicts should return 409")

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Equal(t, "port_conflict", resp["error"])
	conflicts := resp["conflicts"].([]interface{})
	assert.Len(t, conflicts, 1, "should have one conflict")
}

// ============================================================================
// Regression Tests for Bug 2.9: Incomplete Rollback on Engine Failure
// ============================================================================

// mockFailingEngineServer creates a test server that fails on certain operations.
type mockFailingEngineServer struct {
	*httptest.Server
	mocks       map[string]*config.MockConfiguration
	failOnMock  string // ID of mock to fail on
	failMessage string
}

func newMockFailingEngineServer(failOnMock, failMessage string) *mockFailingEngineServer {
	mes := &mockFailingEngineServer{
		mocks:       make(map[string]*config.MockConfiguration),
		failOnMock:  failOnMock,
		failMessage: failMessage,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(engineclient.StatusResponse{
			ID:     "test-engine",
			Status: "running",
		})
	})

	mux.HandleFunc("GET /mocks", func(w http.ResponseWriter, r *http.Request) {
		mocks := make([]*config.MockConfiguration, 0, len(mes.mocks))
		for _, m := range mes.mocks {
			mocks = append(mocks, m)
		}
		json.NewEncoder(w).Encode(engineclient.MockListResponse{
			Mocks: mocks,
			Count: len(mocks),
		})
	})

	mux.HandleFunc("GET /mocks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		m, ok := mes.mocks[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(ErrorResponse{Error: "not_found"})
			return
		}
		json.NewEncoder(w).Encode(m)
	})

	mux.HandleFunc("POST /mocks", func(w http.ResponseWriter, r *http.Request) {
		var m config.MockConfiguration
		json.NewDecoder(r.Body).Decode(&m)

		if m.ID == mes.failOnMock {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   "engine_error",
				Message: mes.failMessage,
			})
			return
		}

		mes.mocks[m.ID] = &m
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(&m)
	})

	mux.HandleFunc("DELETE /mocks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		delete(mes.mocks, id)
		w.WriteHeader(http.StatusNoContent)
	})

	mes.Server = httptest.NewServer(mux)
	return mes
}

func (mes *mockFailingEngineServer) client() *engineclient.Client {
	return engineclient.New(mes.URL)
}

func TestCreateMock_EngineFailure_RollsBack(t *testing.T) {
	tmpDir := t.TempDir()

	// Engine that fails on creating this specific mock
	server := newMockFailingEngineServer("will-fail", "port 8080 is already in use")
	defer server.Close()

	api := NewAPI(0,
		WithDataDir(tmpDir),
		WithLocalEngineClient(server.client()),
	)
	defer api.Stop()

	// Wait for store to initialize
	time.Sleep(50 * time.Millisecond)

	mockData := &mock.Mock{
		ID:   "will-fail",
		Name: "Mock That Fails",
		Type: mock.TypeHTTP,
		HTTP: &mock.HTTPSpec{
			Matcher:  &mock.HTTPMatcher{Method: "GET", Path: "/test"},
			Response: &mock.HTTPResponse{StatusCode: 200},
		},
	}
	body, _ := json.Marshal(mockData)

	req := httptest.NewRequest("POST", "/mocks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	api.handleCreateUnifiedMock(rec, req)

	// Should return error (port conflict from engine)
	assert.Equal(t, http.StatusConflict, rec.Code,
		"should return 409 on port error from engine")

	// Verify mock was NOT persisted in the store (rollback)
	mockStore := api.getMockStore()
	require.NotNil(t, mockStore)

	_, err := mockStore.Get(context.Background(), "will-fail")
	assert.Equal(t, store.ErrNotFound, err,
		"mock should be rolled back from store after engine failure")
}
