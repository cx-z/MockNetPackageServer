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

// TestMockNetPackWebExemptFromAPIKey  verifies the API-key middleware
// exempts the /mocknetpack/ web shell for a LAN browser (non-loopback
// RemoteAddr) with auth enabled, while the legacy mockd admin API stays
// protected. Before this exemption every LAN /mocknetpack/ request was 401
// missing_api_key because the directory URL has no static-asset extension.
func TestMockNetPackWebExemptFromAPIKey(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>mocknetpack-lan</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "app.js"), []byte("console.log('app')"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Auth mode (API key enabled) + LAN remote address: the browser shape that
	// previously got 401 on the directory URL.
	api := NewAPI(0, WithDataDir(t.TempDir()), WithAPIKey("test-api-key"), WithWebDir(webDir))
	t.Cleanup(func() { api.Stop() })

	lan := func(method, path string, apiKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "192.168.25.18:12345" // LAN browser, not loopback
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		return serveAdmin(t, api, req)
	}

	// 1. Directory URL from LAN → 200 (the  fix; was 401 missing_api_key).
	rec := lan(http.MethodGet, "/mocknetpack/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("LAN /mocknetpack/ = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<html>mocknetpack-lan</html>" {
		t.Fatalf("LAN /mocknetpack/ body = %q, want index.html", got)
	}

	// 2. Static asset from LAN → 200.
	if rec := lan(http.MethodGet, "/mocknetpack/app.js", ""); rec.Code != http.StatusOK {
		t.Fatalf("LAN /mocknetpack/app.js = %d, want 200", rec.Code)
	}

	// 3. Legacy admin API from LAN without key → still 401 (protection intact).
	if rec := lan(http.MethodGet, "/openapi.json", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("LAN /openapi.json without API key = %d, want 401", rec.Code)
	}

	// 4. Legacy admin API from LAN with valid API key → still reachable.
	if rec := lan(http.MethodGet, "/openapi.json", "test-api-key"); rec.Code == http.StatusUnauthorized {
		t.Fatal("LAN /openapi.json with valid API key must not be 401")
	}
}

// TestAuthMockNetPackNoSlashExempt（B 审缺口）：auth 模式 + LAN 下访问
// /mocknetpack（无尾斜杠，ServeMux 会 301 到 /mocknetpack/）也不得被
// api-key 中间件 401——验证目录路径的精确豁免与 301 重定向路径的组合。
func TestAuthMockNetPackNoSlashExempt(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>mocknetpack-noslash</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	api := NewAPI(0, WithDataDir(t.TempDir()), WithAPIKey("test-api-key"), WithWebDir(webDir))
	t.Cleanup(func() { api.Stop() })

	req := httptest.NewRequest(http.MethodGet, "/mocknetpack", nil)
	req.RemoteAddr = "192.168.25.18:12345" // LAN browser, not loopback
	rec := serveAdmin(t, api, req)

	// 关键断言：不得 401（豁免生效）。具体状态码应为 301（ServeMux 重定向到
	// 带尾斜杠目录），此处只锁死「不被 api-key 拦截」这一不变量，不绑定 301/200。
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("LAN GET /mocknetpack (no slash) = 401 missing_api_key, want exempt; body=%s", rec.Body.String())
	}
}
