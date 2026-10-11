// Protocol handler registration for SSE and WebSocket.

package engine

import (
	"encoding/json"
	"net/http"

	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/sse"
	"github.com/getmockd/mockd/pkg/websocket"
)

// SSEHandler returns the SSE handler for admin API access.
func (h *Handler) SSEHandler() *sse.SSEHandler { return h.sseHandler }

// ChunkedHandler returns the chunked transfer handler.
func (h *Handler) ChunkedHandler() *sse.ChunkedHandler { return h.chunkedHandler }

// WebSocketManager returns the WebSocket connection manager.
func (h *Handler) WebSocketManager() *websocket.ConnectionManager { return h.wsManager }

// handleWebSocket handles WebSocket upgrade requests.
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	endpoint := h.wsManager.GetEndpoint(r.URL.Path)
	if endpoint == nil {
		pathJSON, _ := json.Marshal(r.URL.Path)
		http.Error(w, `{"error": "websocket_endpoint_not_found", "path": `+string(pathJSON)+`}`, http.StatusNotFound)
		return
	}
	if err := endpoint.HandleUpgrade(w, r); err != nil {
		return
	}
}

// RegisterWebSocketEndpoint registers a WebSocket endpoint from config.
func (h *Handler) RegisterWebSocketEndpoint(cfg *config.WebSocketEndpointConfig) error {
	endpoint, err := websocket.EndpointFromConfig(cfg)
	if err != nil {
		return err
	}
	// Wire template engine so WebSocket responses can use template variables
	if h.templateEngine != nil {
		endpoint.SetTemplateEngine(h.templateEngine)
	}
	h.wsManager.RegisterEndpoint(endpoint)
	return nil
}

// UnregisterWebSocketEndpoint removes a WebSocket endpoint by path.
func (h *Handler) UnregisterWebSocketEndpoint(path string) {
	h.wsManager.UnregisterEndpoint(path)
}

// DisconnectWebSocketEndpoint closes all active connections on a WebSocket endpoint.
// Uses RFC 6455 close code 1012 (Service Restart) so clients reconnect automatically.
// Must be called before UnregisterWebSocketEndpoint while byEndpoint still tracks the path.
func (h *Handler) DisconnectWebSocketEndpoint(path string) {
	h.wsManager.DisconnectByEndpoint(path, websocket.CloseServiceRestart, "mock updated")
}

// DisconnectSSEByMock closes all active SSE connections for the given mock ID.
// Cancelling each connection's context causes the event loop to exit, the response
// writer to close, and the client to receive EOF. EventSource clients will
// auto-reconnect and pick up the new configuration.
func (h *Handler) DisconnectSSEByMock(mockID string) {
	if h.sseHandler == nil {
		return
	}
	mgr := h.sseHandler.GetManager()
	if mgr == nil {
		return
	}
	mgr.CloseByMock(mockID)
}
