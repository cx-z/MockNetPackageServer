package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"github.com/getmockd/mockd/pkg/audit"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiddlewareChain_WebSocketUpgrade verifies optional response wrappers preserve
// WebSocket upgrades, ordinary HTTP responses, and unknown-endpoint errors.
func TestMiddlewareChain_WebSocketUpgrade(t *testing.T) {
	const spec = `openapi: "3.0.3"
info:
  title: WebSocket middleware regression
  version: "1.0.0"
paths:
  /ws/echo:
    get:
      responses:
        "101":
          description: WebSocket upgrade
  /ws/missing:
    get:
      responses:
        "404":
          description: Unknown endpoint
  /__mockd/health:
    get:
      responses:
        "200":
          description: Healthy
`
	for _, tc := range []struct {
		name       string
		audit      bool
		validation bool
	}{
		{name: "default"},
		{name: "audit", audit: true},
		{name: "response validation", validation: true},
		{name: "audit and response validation", audit: true, validation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.ServerConfiguration{}
			if tc.audit {
				cfg.Audit = &audit.AuditConfig{
					Enabled:    true,
					OutputFile: filepath.Join(t.TempDir(), "audit.jsonl"),
				}
			}
			if tc.validation {
				cfg.Validation = &validation.ValidationConfig{
					Enabled:          true,
					Spec:             spec,
					ValidateRequest:  true,
					ValidateResponse: true,
					FailOnError:      true,
					LogWarnings:      true,
				}
			}
			chain, err := NewMiddlewareChain(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, chain.Close()) })

			handler := newTestHandler()
			t.Cleanup(handler.WebSocketManager().Close)
			require.NoError(t, handler.RegisterWebSocketEndpoint(&config.WebSocketEndpointConfig{
				Path: "/ws/echo",
			}))
			server := httptest.NewServer(chain.Wrap(handler))
			t.Cleanup(server.Close)

			resp, err := server.Client().Get(server.URL + "/__mockd/health")
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, resp.Body.Close())

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			conn, resp, err := ws.Dial(ctx, wsURL+"/ws/echo", nil)
			if resp != nil && resp.Body != nil {
				defer resp.Body.Close()
			}
			require.NoError(t, err)
			defer conn.CloseNow()
			require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

			require.NoError(t, conn.Write(ctx, ws.MessageText, []byte("hello through wrappers")))
			messageType, message, err := conn.Read(ctx)
			require.NoError(t, err)
			assert.Equal(t, ws.MessageText, messageType)
			assert.Equal(t, "hello through wrappers", string(message))

			_, resp, err = ws.Dial(ctx, wsURL+"/ws/missing", nil)
			if resp != nil && resp.Body != nil {
				defer resp.Body.Close()
			}
			require.Error(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		})
	}
}
