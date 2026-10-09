package mnpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliFake is a minimal mocknetpack API for CLI tests.
type cliFake struct {
	ts *httptest.Server
}

func newCLIFake(t *testing.T, mux *http.ServeMux) *cliFake {
	t.Helper()
	f := &cliFake{ts: httptest.NewServer(mux)}
	t.Cleanup(f.ts.Close)
	return f
}

func writeJ(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// runCLI executes Run with the fake server and returns (exit code, stdout, stderr).
// A nil fake skips the --server flag (usage-only invocations).
func runCLI(f *cliFake, env map[string]string, args ...string) (int, string, string) {
	ctx := context.Background()
	var out, errb bytes.Buffer
	full := args
	if f != nil {
		full = append([]string{"--server", f.ts.URL}, args...)
	}
	oldEnv := os.Getenv("MOCKNETPACK_API_KEY")
	for k, v := range env {
		_ = os.Setenv(k, v)
	}
	defer os.Setenv("MOCKNETPACK_API_KEY", oldEnv)
	code := Run(ctx, full, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLI_Help_ExitZero(t *testing.T) {
	code, out, _ := runCLI(nil, nil, "--help")
	assert.Equal(t, 0, code)
	for _, want := range []string{"devices list", "traffic list", "traffic export", "traffic get",
		"share create", "share get", "rule create-from-traffic", "rule create", "rule set-enabled", "rule update",
		"MOCKNETPACK_API_KEY", "退出码"} {
		assert.Contains(t, out, want, "help must document %q", want)
	}
}

func TestCLI_MissingAPIKey_Exit2(t *testing.T) {
	code, _, errOut := runCLI(nil, nil, "devices", "list")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "API Key")
}

func TestCLI_APIKeyPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer key-from-flag", r.Header.Get("Authorization"))
		writeJ(w, 200, map[string]any{"devices": []any{}, "total": 0})
	})
	f := newCLIFake(t, mux)

	// --api-key flag path.
	code, out, _ := runCLI(f, nil, "--api-key", "key-from-flag", "devices", "list")
	assert.Equal(t, 0, code)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, float64(0), parsed["total"])

	// MOCKNETPACK_API_KEY env path.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer key-from-env", r.Header.Get("Authorization"))
		writeJ(w, 200, map[string]any{"devices": []any{}, "total": 0})
	})
	f2 := newCLIFake(t, mux2)
	code, _, _ = runCLI(f2, map[string]string{"MOCKNETPACK_API_KEY": "key-from-env"}, "devices", "list")
	assert.Equal(t, 0, code)
}

func TestCLI_TrafficList_CompactByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJ(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	var gotQuery string
	mux.HandleFunc("/api/v1/sessions/s1/traffic", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		writeJ(w, 200, map[string]any{
			"entries": []capture.TrafficEntry{{ID: "t1", Method: "GET", URL: "https://a.com/x", Seq: 1}},
			"total":   1,
		})
	})
	f := newCLIFake(t, mux)

	code, out, _ := runCLI(f, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"traffic", "list", "--app", "com.a", "--did", "d1", "--scheme", "https", "--since", "3")
	assert.Equal(t, 0, code)
	assert.Contains(t, gotQuery, "projection=compact", "traffic list defaults to compact")
	assert.Contains(t, gotQuery, "scheme=https")
	assert.Contains(t, gotQuery, "since=3")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "s1", parsed["sessionId"])
}

func TestCLI_TrafficExport_WritesConsumableFile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJ(w, 200, map[string]any{
			"sessions": []capture.CaptureSession{{ID: "s1", Status: capture.SessionStatusCapturing}},
			"total":    1,
		})
	})
	mux.HandleFunc("/api/v1/sessions/s1/traffic", func(w http.ResponseWriter, r *http.Request) {
		writeJ(w, 200, map[string]any{
			"entries": []capture.TrafficEntry{
				{ID: "t1", Method: "GET", URL: "https://a.com/1", Seq: 1, Mocked: true},
				{ID: "t2", Method: "POST", URL: "http://a.com/2", Seq: 2},
			},
			"total": 2,
		})
	})
	f := newCLIFake(t, mux)
	dest := filepath.Join(t.TempDir(), "traffic.json")

	code, out, _ := runCLI(f, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"traffic", "export", "--app", "com.a", "--did", "d1", "--since", "5", "-o", dest)
	assert.Equal(t, 0, code)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, float64(2), parsed["exported"])

	// C5: the file is a JSON array directly consumable by jq/grep.
	b, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(string(b)), "["), "export file must be a JSON array")
	var entries []capture.TrafficEntry
	require.NoError(t, json.Unmarshal(b, &entries))
	require.Len(t, entries, 2)
	assert.Equal(t, int64(1), entries[0].Seq)
	assert.True(t, entries[0].Mocked)
}

func TestCLI_ExitCodes(t *testing.T) {
	// Usage error → 2.
	code, _, errOut := runCLI(nil, map[string]string{"MOCKNETPACK_API_KEY": "k"}, "traffic", "list")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "--app 与 --did 必填")

	// Unknown subcommand → 2.
	code, _, _ = runCLI(nil, nil, "bogus")
	assert.Equal(t, 2, code)

	// Business error (server 404) → 1, message on stderr as JSON.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJ(w, 404, map[string]string{"error": "not_found", "message": "会话不存在"})
	})
	f := newCLIFake(t, mux)
	code, _, errOut = runCLI(f, map[string]string{"MOCKNETPACK_API_KEY": "k"}, "traffic", "list", "--app", "com.a", "--did", "d1")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "会话不存在")
	assert.Contains(t, errOut, "先查询确认 id 有效")
}

func TestCLI_RuleCreateFromTraffic_NoteRequired(t *testing.T) {
	// Empty note is a usage error ( 语义透传).
	code, _, errOut := runCLI(nil, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"rule", "create-from-traffic", "t1", "--app", "com.a", "--did", "d1", "--note", "  ")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "note 必填")
}

func TestCLI_RuleSetEnabled_RequiresExactlyOne(t *testing.T) {
	code, _, errOut := runCLI(nil, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"rule", "set-enabled", "r1", "--app", "com.a", "--did", "d1")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "--enable 或 --disable")

	code, _, errOut = runCLI(nil, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"rule", "set-enabled", "r1", "--app", "com.a", "--did", "d1", "--enable", "--disable")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "--enable 或 --disable")
}

func TestCLI_ShareCreate_JSONOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/shares", func(w http.ResponseWriter, r *http.Request) {
		writeJ(w, 201, map[string]any{"shareId": "share-9", "url": "/mocknetpack/#/share/share-9", "expiresAt": "2026-10-14T00:00:00Z"})
	})
	f := newCLIFake(t, mux)
	code, out, _ := runCLI(f, map[string]string{"MOCKNETPACK_API_KEY": "k"}, "share", "create", "t1")
	assert.Equal(t, 0, code)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "share-9", parsed["shareId"])
}

// TestCLI_RuleSetEnabled_CarriesFullResponse: the server PUT is a full replace
// (response.statusCode 100–599 validated), so a toggle must read-modify-write.
func TestCLI_RuleSetEnabled_CarriesFullResponse(t *testing.T) {
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJ(w, 200, map[string]any{
			"version": 3,
			"rules": []map[string]any{{
				"id": "r1", "app": "com.a", "did": "d1", "method": "GET", "path": "/x",
				"response": map[string]any{"statusCode": 200, "headers": map[string]string{"X-A": "1"}, "body": "ok"},
				"enabled":  false, "note": "keep-me", "effective": false,
			}},
		})
	})
	mux.HandleFunc("/api/v1/devices/com.a/d1/mock-rules/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJ(w, 200, map[string]any{
			"id": "r1", "app": "com.a", "did": "d1", "method": "GET", "path": "/x",
			"response": gotBody["response"], "enabled": gotBody["enabled"], "note": gotBody["note"],
			"effective": gotBody["enabled"] == true,
		})
	})
	f := newCLIFake(t, mux)
	code, out, errOut := runCLI(f, map[string]string{"MOCKNETPACK_API_KEY": "k"},
		"rule", "set-enabled", "r1", "--app", "com.a", "--did", "d1", "--enable")
	assert.Equal(t, 0, code, errOut)
	resp, _ := gotBody["response"].(map[string]any)
	require.NotNil(t, resp, "PUT body must carry the full response")
	assert.Equal(t, float64(200), resp["statusCode"])
	assert.Equal(t, "keep-me", gotBody["note"])
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, true, parsed["enabled"])
}
