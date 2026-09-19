package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestMockNetPackWebRoute verifies the /mocknetpack/ static route serves the
// web UI and that the capture API routes still take priority (SPA assets are
// served without shadowing /api/v1).
func TestMockNetPackWebRoute(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>mocknetpack</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "app.js"), []byte("console.log('app')"), 0o644); err != nil {
		t.Fatal(err)
	}

	api := NewAPI(0, WithDataDir(t.TempDir()), WithAPIKeyDisabled(), WithWebDir(webDir))
	ts := httptest.NewServer(api.httpServer.Handler)
	defer ts.Close()

	// 1. index.html is served at /mocknetpack/
	res, err := http.Get(ts.URL + "/mocknetpack/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /mocknetpack/ = %d, want 200", res.StatusCode)
	}
	if string(body) != "<html>mocknetpack</html>" {
		t.Fatalf("unexpected body: %q", body)
	}

	// 2. Static asset is served
	res2, err := http.Get(ts.URL + "/mocknetpack/app.js")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("GET /mocknetpack/app.js = %d, want 200", res2.StatusCode)
	}

	// 3. Missing asset returns 404 (FileServer behavior), not index fallback
	res3, err := http.Get(ts.URL + "/mocknetpack/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /mocknetpack/missing.js = %d, want 404", res3.StatusCode)
	}

	// 4. Capture API route still works (not shadowed by static prefix)
	res4, err := http.Get(ts.URL + "/api/v1/devices")
	if err != nil {
		t.Fatal(err)
	}
	res4.Body.Close()
	if res4.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/devices = %d, want 200", res4.StatusCode)
	}
}
