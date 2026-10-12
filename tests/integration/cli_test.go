package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCLIStartCommand tests the mockd start command lifecycle.
func TestCLIStartCommand(t *testing.T) {
	// Find available ports
	httpPort := findAvailablePort(t, 18080)
	adminPort := findAvailablePort(t, 19090)

	// Create isolated data directory for this test
	dataDir := t.TempDir()

	// Build the CLI binary
	buildCmd := exec.Command("go", "build", "-o", "mockd_test", "./cmd/mockd")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build CLI: %v\n%s", err, out)
	}
	defer exec.Command("rm", "-f", "../../mockd_test").Run()

	// Start the server in background with isolated data directory
	// Use 120s timeout to allow for slow CI environments with -race builds
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "./mockd_test", "start",
		"--port", fmt.Sprintf("%d", httpPort),
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--no-auth",
		"--data-dir", dataDir,
	)
	cmd.Dir = "../.."

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	// Wait for server to be ready
	adminURL := fmt.Sprintf("http://localhost:%d", adminPort)
	if !waitForServer(adminURL+"/health", 60*time.Second) {
		cmd.Process.Kill()
		t.Fatalf("Server did not become ready in time\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}

	// Verify admin API responds
	resp, err := http.Get(adminURL + "/health")
	if err != nil {
		cmd.Process.Kill()
		t.Fatalf("Failed to reach admin API: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cmd.Process.Kill()
		t.Fatalf("Expected 200 from health endpoint, got %d", resp.StatusCode)
	}

	// Verify mock server responds
	mockURL := fmt.Sprintf("http://localhost:%d", httpPort)
	resp, err = http.Get(mockURL + "/")
	if err != nil {
		cmd.Process.Kill()
		t.Fatalf("Failed to reach mock server: %v", err)
	}
	resp.Body.Close()
	// 404 is expected since no mocks configured
	if resp.StatusCode != http.StatusNotFound {
		cmd.Process.Kill()
		t.Fatalf("Expected 404 from mock server (no mocks), got %d", resp.StatusCode)
	}

	// Kill the server
	cmd.Process.Kill()
	cmd.Wait()

	t.Log("CLI start command test passed")
}

// TestCLIMockCRUDCommands tests add, list, get, delete commands.
func TestCLIMockCRUDCommands(t *testing.T) {
	// Find available ports
	httpPort := findAvailablePort(t, 28080)
	adminPort := findAvailablePort(t, 29090)

	// Create isolated data directory for this test
	dataDir := t.TempDir()

	// Build the CLI binary
	buildCmd := exec.Command("go", "build", "-o", "mockd_test", "./cmd/mockd")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build CLI: %v\n%s", err, out)
	}
	defer exec.Command("rm", "-f", "../../mockd_test").Run()

	// Start the server in background with isolated data directory
	// Use 120s timeout to allow for slow CI environments (GitHub shared runners)
	// where server startup with -race can take 30-40s.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, "./mockd_test", "start",
		"--port", fmt.Sprintf("%d", httpPort),
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--no-auth",
		"--data-dir", dataDir,
	)
	serverCmd.Dir = "../.."
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer serverCmd.Process.Kill()

	// Wait for server to be ready
	adminURL := fmt.Sprintf("http://localhost:%d", adminPort)
	if !waitForServer(adminURL+"/health", 60*time.Second) {
		t.Fatal("Server did not become ready in time")
	}

	// Test add command
	t.Run("add", func(t *testing.T) {
		addCmd := exec.Command("./mockd_test", "add",
			"--path", "/api/test",
			"--status", "201",
			"--body", `{"message": "created"}`,
			"-H", "Content-Type:application/json",
			"--admin-url", adminURL,
		)
		addCmd.Dir = "../.."
		out, err := addCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Add command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Created mock:") {
			t.Fatalf("Expected 'Created mock:' in output, got: %s", out)
		}
	})

	// Test list command
	var mockID string
	t.Run("list", func(t *testing.T) {
		listCmd := exec.Command("./mockd_test", "list", "--json", "--admin-url", adminURL)
		listCmd.Dir = "../.."
		out, err := listCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("List command failed: %v\n%s", err, out)
		}

		var mocks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(out, &mocks); err != nil {
			t.Fatalf("Failed to parse list output: %v\n%s", err, out)
		}
		if len(mocks) == 0 {
			t.Fatal("Expected at least one mock in list")
		}
		mockID = mocks[0].ID
	})

	// Test get command
	t.Run("get", func(t *testing.T) {
		if mockID == "" {
			t.Skip("No mock ID from list test")
		}
		getCmd := exec.Command("./mockd_test", "get", mockID, "--admin-url", adminURL)
		getCmd.Dir = "../.."
		out, err := getCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Get command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "/api/test") {
			t.Fatalf("Expected '/api/test' in output, got: %s", out)
		}
	})

	// Test delete command
	t.Run("delete", func(t *testing.T) {
		if mockID == "" {
			t.Skip("No mock ID from list test")
		}
		delCmd := exec.Command("./mockd_test", "delete", mockID, "--admin-url", adminURL)
		delCmd.Dir = "../.."
		out, err := delCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Delete command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Deleted mock:") {
			t.Fatalf("Expected 'Deleted mock:' in output, got: %s", out)
		}
	})

	// Verify mock was deleted
	t.Run("verify_deleted", func(t *testing.T) {
		if mockID == "" {
			t.Skip("No mock ID from list test")
		}

		listCmd := exec.Command("./mockd_test", "list", "--json", "--admin-url", adminURL)
		listCmd.Dir = "../.."
		out, err := listCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("List command failed: %v\n%s", err, out)
		}

		var mocks []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(out, &mocks); err != nil {
			t.Fatalf("Failed to parse list output: %v\n%s", err, out)
		}

		// Verify the specific mock we deleted is no longer in the list
		for _, m := range mocks {
			if m.ID == mockID {
				t.Fatalf("Mock %s should have been deleted but still exists", mockID)
			}
		}
	})
}

// TestCLIVersionCommand tests the version command.
func TestCLIVersionCommand(t *testing.T) {
	// Build the CLI binary
	buildCmd := exec.Command("go", "build", "-o", "mockd_test", "./cmd/mockd")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build CLI: %v\n%s", err, out)
	}
	defer exec.Command("rm", "-f", "../../mockd_test").Run()

	t.Run("text_output", func(t *testing.T) {
		cmd := exec.Command("./mockd_test", "version")
		cmd.Dir = "../.."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Version command failed: %v\n%s", err, out)
		}
		// Output format is "mockd v{version} ({commit}, {date})"
		if !strings.HasPrefix(string(out), "mockd v") {
			t.Fatalf("Expected 'mockd v' prefix, got: %s", out)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		cmd := exec.Command("./mockd_test", "version", "--json")
		cmd.Dir = "../.."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Version --json command failed: %v\n%s", err, out)
		}

		var version struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(out, &version); err != nil {
			t.Fatalf("Failed to parse version JSON: %v\n%s", err, out)
		}
		if version.Version == "" {
			t.Fatal("Expected non-empty version field")
		}
	})
}

// TestCLIRemovedCommands verifies intentionally removed commands remain unavailable.
func TestCLIRemovedCommands(t *testing.T) {
	binaryPath := buildBinary(t)
	for _, command := range []string{"init", "import", "export"} {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(binaryPath, command, "--help")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("Removed command %s unexpectedly succeeded", command)
			}
			if !strings.Contains(string(out), "unknown command \""+command+"\"") {
				t.Fatalf("Expected unknown command error for %s, got: %s", command, out)
			}
		})
	}
}

// TestCLIHelpOutput tests that all commands have help.
func TestCLIHelpOutput(t *testing.T) {
	// Build the CLI binary
	buildCmd := exec.Command("go", "build", "-o", "mockd_test", "./cmd/mockd")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build CLI: %v\n%s", err, out)
	}
	defer exec.Command("rm", "-f", "../../mockd_test").Run()

	commands := []string{"start", "add", "list", "get", "delete", "logs", "config", "completion", "version"}

	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			helpCmd := exec.Command("./mockd_test", cmd, "--help")
			helpCmd.Dir = "../.."
			out, _ := helpCmd.CombinedOutput()
			// --help returns exit code 1 with flag package, so we don't check error
			if !strings.Contains(string(out), "Usage:") {
				t.Errorf("Expected 'Usage:' in help output for %s, got: %s", cmd, out)
			}
		})
	}
}

// findAvailablePort finds an available TCP port safely, ignoring the start range.
func findAvailablePort(t *testing.T, start int) int {
	return GetFreePortSafe()
}

// waitForServer waits for a server to become available.
func waitForServer(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond) // Polling interval
	}
	return false
}
