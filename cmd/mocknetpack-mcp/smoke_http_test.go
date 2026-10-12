package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSmokeHTTP_ClosedLoop runs the actual Python smoke through Streamable HTTP
// against isolated real admin/storage endpoints. The SDK fixture pulls an
// effective rule and uploads its simulated mock hit; it does not run an iOS SDK.
func TestSmokeHTTP_ClosedLoop(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python smoke integration requires python3")
	}
	cases := []struct {
		shared  bool
		history int
	}{{false, 1}, {true, 1}, {false, 1000}, {true, 1000}}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("existing_default_observer=%t/history=%d", tc.shared, tc.history), func(t *testing.T) {
			base := standardBackend(t)
			seedOnlineDevice(t, base)
			srv := newMCPServer(base)
			client := mnpapi.NewClient(base, testKey)
			active, _, activateErr := client.ActivateSession(context.Background(), testApp, testDid)
			require.NoError(t, activateErr)
			if tc.shared {
				mustResultJSON(t, callSessionTool(t, srv, "start_capture", nil))
			}
			// Batch history crosses the compact API's 500-row page boundary.
			entries := make([]capture.TrafficEntry, tc.history)
			for i := range entries {
				entries[i] = capture.TrafficEntry{Method: "GET", URL: "https://example.com/api/feed",
					Path: "/api/feed", StatusCode: 200, Timestamp: time.Now()}
			}
			for offset := 0; offset < len(entries); offset += 500 {
				end := min(offset+500, len(entries))
				upload := rawJSON(t, http.MethodPost, base+"/api/v1/traffic", map[string]any{
					"app": testApp, "did": testDid, "sessionId": active.ID, "entries": entries[offset:end],
				})
				require.Equal(t, http.StatusAccepted, upload.StatusCode)
				upload.Body.Close()
			}
			made := mustResultJSON(t, callSessionTool(t, srv, "create_mock_rule", map[string]any{
				"method": "GET", "path": "/keep", "statusCode": float64(200), "body": "keep", "note": "pre-smoke snapshot",
			}))
			keepID := made["rule"].(map[string]any)["id"].(string)
			mustResultJSON(t, callSessionTool(t, srv, "set_mock_rule_enabled", map[string]any{"ruleId": keepID, "enabled": true}))

			gateway := httptest.NewServer(authGateway(server.NewStreamableHTTPServer(srv,
				server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
					return context.WithValue(ctx, ctxBearerKey, keyFromAuthorization(r))
				}))))
			t.Cleanup(gateway.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sdkDone := make(chan error, 1)
			go func() { sdkDone <- uploadPulledMockHit(ctx, base, active.ID) }()
			cmd := exec.CommandContext(ctx, python, "smoke_mcp_http.py")
			cmd.Env = append(os.Environ(), "MNP_KEY="+testKey, "MNP_URL="+gateway.URL+"/mcp",
				"MNP_APP="+testApp, "MNP_DID="+testDid, "MNP_SKIP_SESSION=0", "MNP_HIT_TIMEOUT=5")
			output, runErr := cmd.CombinedOutput()
			cancel()
			require.NoError(t, runErr, "%s", output)
			require.NoError(t, <-sdkDone)
			t.Logf("real HTTP smoke (simulated SDK hit):\n%s", output)
			assert.Contains(t, string(output), "mock hit verified")
			assert.Contains(t, string(output), "OK smoke passed")

			after, getErr := client.GetSession(context.Background(), active.ID)
			require.NoError(t, getErr)
			if tc.shared {
				assert.Equal(t, capture.SessionStatusCapturing, after.Status)
				assert.Equal(t, 1, after.ViewerCount, "smoke must preserve the existing default lease")
			} else {
				assert.Equal(t, capture.SessionStatusEnded, after.Status)
				assert.NotNil(t, after.RetainUntil)
			}
			rules, listErr := client.ListMockRules(context.Background(), testApp, testDid)
			require.NoError(t, listErr)
			require.Len(t, rules, 1, "temporary smoke rule must be deleted")
			assert.Equal(t, keepID, rules[0].ID)
			assert.True(t, rules[0].Enabled, "the pre-run enabled-rule snapshot must be restored")
		})
	}
}

// uploadPulledMockHit simulates an SDK using only the production SDK pull and
// traffic-upload HTTP endpoints. It waits for the smoke rule to become effective
// before uploading a hit with that rule's ID and response on the held session.
func uploadPulledMockHit(ctx context.Context, base, sid string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+"/api/v1/devices/"+testApp+"/"+testDid+"/mock-rules?sinceVersion=0", nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		var pulled struct {
			Rules []capture.MockRule `json:"rules"`
		}
		decodeErr := json.NewDecoder(res.Body).Decode(&pulled)
		res.Body.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("SDK pull returned %d", res.StatusCode)
		}
		for _, rule := range pulled.Rules {
			if rule.Path != "/api/feed" || rule.Method != "GET" {
				continue
			}
			body, marshalErr := json.Marshal(map[string]any{
				"app": testApp, "did": testDid, "sessionId": sid,
				"entries": []capture.TrafficEntry{{
					Timestamp: time.Now(), Method: rule.Method, Path: rule.Path,
					URL: "https://example.com" + rule.Path, StatusCode: rule.Response.StatusCode,
					ResponseBody: rule.Response.Body, Mocked: true, MatchedRuleID: rule.ID,
				}},
			})
			if marshalErr != nil {
				return marshalErr
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/traffic", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			res, err = http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			res.Body.Close()
			if res.StatusCode != http.StatusAccepted {
				return fmt.Errorf("SDK hit upload returned %d", res.StatusCode)
			}
			return nil
		}
	}
}
