package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/admin"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getTestProtoFile returns the absolute path to the test proto file
func getTestProtoFile() string {
	_, currentFile, _, _ := runtime.Caller(0)
	testDir := filepath.Dir(currentFile)
	return filepath.Join(testDir, "..", "fixtures", "grpc", "test.proto")
}

// getUniquePort returns a unique port - wrapper around shared helper
func getUniquePort() int {
	return GetFreePortSafe()
}

// ============================================================================
// Test Helpers
// ============================================================================

// portConflictTestBundle groups server and admin API for port conflict tests
type portConflictTestBundle struct {
	Server         *engine.Server
	AdminAPI       *admin.API
	HTTPPort       int
	AdminPort      int
	ManagementPort int
}

func setupPortConflictServer(t *testing.T) *portConflictTestBundle {
	// Use unique ports to avoid conflicts with parallel tests
	httpPort := getUniquePort()
	adminPort := getUniquePort()
	managementPort := getUniquePort()

	cfg := &config.ServerConfiguration{
		HTTPPort:       httpPort,
		AdminPort:      adminPort,
		ManagementPort: managementPort,
		ReadTimeout:    30,
		WriteTimeout:   30,
	}

	srv := engine.NewServer(cfg)
	err := srv.Start()
	require.NoError(t, err)

	// Create our own temp dir so we can control cleanup timing
	dataDir, err := os.MkdirTemp("", "mockd-port-conflict-test-*")
	require.NoError(t, err)

	// Create admin API with data dir for persistence
	adminAPI := admin.NewAPI(adminPort,
		admin.WithLocalEngine(fmt.Sprintf("http://localhost:%d", srv.ManagementPort())),
		admin.WithAPIKeyDisabled(),
		admin.WithDataDir(dataDir),
	)
	err = adminAPI.Start()
	require.NoError(t, err)

	t.Cleanup(func() {
		adminAPI.Stop()
		srv.Stop()
		// Give time for file handles to close
		time.Sleep(50 * time.Millisecond)
		// Remove temp dir after everything is stopped
		os.RemoveAll(dataDir)
	})

	waitForReady(t, managementPort)
	waitForReady(t, adminPort)

	return &portConflictTestBundle{
		Server:         srv,
		AdminAPI:       adminAPI,
		HTTPPort:       httpPort,
		AdminPort:      adminPort,
		ManagementPort: managementPort,
	}
}

// createGRPCMock helper to create a gRPC mock via the admin API
func createGRPCMock(t *testing.T, adminPort int, name string, grpcPort int, workspaceID string) (int, map[string]interface{}) {
	mockData := map[string]interface{}{
		"type":        "grpc",
		"name":        name,
		"enabled":     true,
		"workspaceId": workspaceID,
		"grpc": map[string]interface{}{
			"port":      grpcPort,
			"protoFile": getTestProtoFile(),
		},
	}
	body, _ := json.Marshal(mockData)

	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d/mocks", adminPort),
		"application/json",
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var respData map[string]interface{}
	json.Unmarshal(respBody, &respData)

	return resp.StatusCode, respData
}

// updateMock helper to update a mock via the admin API
func updateMock(t *testing.T, adminPort int, mockID string, mockData map[string]interface{}) (int, map[string]interface{}) {
	body, _ := json.Marshal(mockData)

	req, err := http.NewRequest("PUT", fmt.Sprintf("http://localhost:%d/mocks/%s", adminPort, mockID), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var respData map[string]interface{}
	json.Unmarshal(respBody, &respData)

	return resp.StatusCode, respData
}

// ============================================================================
// Port Conflict Tests
// ============================================================================

func TestPortConflict_HTTPMock_NoConflictCheck(t *testing.T) {
	bundle := setupPortConflictServer(t)

	// Create HTTP mock (should not check port conflicts)
	mockData := map[string]interface{}{
		"type":    "http",
		"name":    "HTTP Endpoint",
		"enabled": true,
		"http": map[string]interface{}{
			"matcher": map[string]interface{}{
				"method": "GET",
				"path":   "/api/test",
			},
			"response": map[string]interface{}{
				"statusCode": 200,
				"body":       "OK",
			},
		},
	}
	body, _ := json.Marshal(mockData)

	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d/mocks?workspaceId=local", bundle.AdminPort),
		"application/json",
		bytes.NewReader(body),
	)
	require.NoError(t, err)

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var respData map[string]interface{}
	json.Unmarshal(respBody, &respData)

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.NotEmpty(t, respData["id"])
}

func TestPortConflict_MultipleGRPCMocks_DifferentPorts(t *testing.T) {
	bundle := setupPortConflictServer(t)

	// Get three free ports using existing helper
	ports := make([]int, 3)
	for i := 0; i < 3; i++ {
		ports[i] = getUniquePort()
		// Ensure uniqueness
		for j := 0; j < i; j++ {
			if ports[i] == ports[j] {
				ports[i] = getUniquePort()
				j = -1 // restart check
			}
		}
	}

	// Create three gRPC mocks on different ports - all should succeed
	for i, port := range ports {
		name := fmt.Sprintf("gRPC Server %d", i+1)
		status, data := createGRPCMock(t, bundle.AdminPort, name, port, "local")
		assert.Equal(t, http.StatusCreated, status, "Failed to create %s", name)
		assert.NotEmpty(t, data["id"])
	}
}

// ============================================================================
// Bulk Create Port Conflict Tests
// ============================================================================

// bulkCreateMocks helper to bulk create mocks via the admin API
func bulkCreateMocks(t *testing.T, adminPort int, mocks []map[string]interface{}) (int, map[string]interface{}) {
	body, _ := json.Marshal(mocks)

	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d/mocks/bulk", adminPort),
		"application/json",
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var respData map[string]interface{}
	json.Unmarshal(respBody, &respData)

	return resp.StatusCode, respData
}

func TestPortConflict_BulkCreate_NoDedicatedPorts_NoConflict(t *testing.T) {
	bundle := setupPortConflictServer(t)

	// Create batch of HTTP mocks - no port conflict checks needed
	mocks := []map[string]interface{}{
		{
			"type":        "http",
			"name":        "HTTP Mock 1",
			"enabled":     true,
			"workspaceId": "local",
			"http": map[string]interface{}{
				"matcher":  map[string]interface{}{"method": "GET", "path": "/api/1"},
				"response": map[string]interface{}{"statusCode": 200},
			},
		},
		{
			"type":        "http",
			"name":        "HTTP Mock 2",
			"enabled":     true,
			"workspaceId": "local",
			"http": map[string]interface{}{
				"matcher":  map[string]interface{}{"method": "GET", "path": "/api/2"},
				"response": map[string]interface{}{"statusCode": 200},
			},
		},
	}

	status, data := bulkCreateMocks(t, bundle.AdminPort, mocks)
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, float64(2), data["created"])
}

// ============================================================================
// Engine Port Error + Rollback Tests
// ============================================================================
