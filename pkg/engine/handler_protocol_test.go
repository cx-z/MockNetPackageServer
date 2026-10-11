package engine

import (
	"testing"

	"github.com/getmockd/mockd/internal/storage"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHandler creates a Handler with an in-memory store for testing.
func newTestHandler() *Handler {
	return NewHandler(storage.NewInMemoryMockStore())
}

// ============================================================================
// WebSocket Endpoint Registration
// ============================================================================

func TestHandlerProtocol_WebSocket_RegisterAndGet(t *testing.T) {
	t.Parallel()

	h := newTestHandler()
	wsCfg := &config.WebSocketEndpointConfig{
		Path: "/ws/chat",
	}

	err := h.RegisterWebSocketEndpoint(wsCfg)
	require.NoError(t, err)

	ep := h.wsManager.GetEndpoint("/ws/chat")
	require.NotNil(t, ep, "endpoint should be registered")
}

func TestHandlerProtocol_WebSocket_RegisterMultiple(t *testing.T) {
	t.Parallel()

	h := newTestHandler()

	err := h.RegisterWebSocketEndpoint(&config.WebSocketEndpointConfig{Path: "/ws/a"})
	require.NoError(t, err)
	err = h.RegisterWebSocketEndpoint(&config.WebSocketEndpointConfig{Path: "/ws/b"})
	require.NoError(t, err)

	assert.NotNil(t, h.wsManager.GetEndpoint("/ws/a"))
	assert.NotNil(t, h.wsManager.GetEndpoint("/ws/b"))
	assert.Nil(t, h.wsManager.GetEndpoint("/ws/c"), "unregistered path returns nil")
}

func TestHandlerProtocol_WebSocket_Unregister(t *testing.T) {
	t.Parallel()

	h := newTestHandler()
	wsCfg := &config.WebSocketEndpointConfig{Path: "/ws/test"}

	err := h.RegisterWebSocketEndpoint(wsCfg)
	require.NoError(t, err)
	require.NotNil(t, h.wsManager.GetEndpoint("/ws/test"))

	h.UnregisterWebSocketEndpoint("/ws/test")

	assert.Nil(t, h.wsManager.GetEndpoint("/ws/test"), "endpoint should be removed")
}

func TestHandlerProtocol_WebSocket_UnregisterNonExistent(t *testing.T) {
	t.Parallel()

	h := newTestHandler()

	assert.NotPanics(t, func() {
		h.UnregisterWebSocketEndpoint("/ws/nope")
	})
}

// ============================================================================
// WebSocket endpoint registration
// ============================================================================

func TestHandlerProtocol_WebSocketEndpointRegistration(t *testing.T) {
	t.Parallel()

	h := newTestHandler()

	wsCfg := &config.WebSocketEndpointConfig{Path: "/ws/api"}
	err := h.RegisterWebSocketEndpoint(wsCfg)
	require.NoError(t, err)

	assert.NotNil(t, h.wsManager.GetEndpoint("/ws/api"))
}

// ============================================================================
// List consistency after register/unregister cycles
// ============================================================================

func TestHandlerProtocol_ListConsistencyAfterCycles(t *testing.T) {
	t.Parallel()

	h := newTestHandler()

	// Cycle: register → unregister → re-register
	wsCfgA := &config.WebSocketEndpointConfig{Path: "/ws/a"}
	wsCfgB := &config.WebSocketEndpointConfig{Path: "/ws/b"}
	require.NoError(t, h.RegisterWebSocketEndpoint(wsCfgA))
	require.NoError(t, h.RegisterWebSocketEndpoint(wsCfgB))
	h.UnregisterWebSocketEndpoint("/ws/a")

	assert.NotNil(t, h.wsManager.GetEndpoint("/ws/b"))
	assert.Nil(t, h.wsManager.GetEndpoint("/ws/a"))
}
