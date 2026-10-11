// Package engine provides the core mock server engine.
package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/getmockd/mockd/internal/id"
	"github.com/getmockd/mockd/internal/storage"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/grpc"
	"github.com/getmockd/mockd/pkg/logging"
	"github.com/getmockd/mockd/pkg/mock"
)

// MockManager handles mock CRUD operations and protocol-specific registration.
type MockManager struct {
	mu              sync.RWMutex
	store           storage.MockStore
	handler         *Handler
	protocolManager *ProtocolManager
	log             *slog.Logger
}

// NewMockManager creates a new mock manager.
func NewMockManager(store storage.MockStore, handler *Handler, pm *ProtocolManager) *MockManager {
	return &MockManager{
		store:           store,
		handler:         handler,
		protocolManager: pm,
		log:             logging.Nop(),
	}
}

// SetLogger sets the logger.
func (mm *MockManager) SetLogger(log *slog.Logger) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if log != nil {
		mm.log = log
	}
}

// Add adds a new mock configuration.
func (mm *MockManager) Add(cfg *config.MockConfiguration) error {
	if cfg == nil {
		return errors.New("mock cannot be nil")
	}

	// Generate ID if not provided
	if cfg.ID == "" {
		cfg.ID = id.UUID()
	}

	// Set timestamps
	now := time.Now()
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = now
	}
	cfg.UpdatedAt = now

	// Set default enabled state
	if cfg.Enabled == nil {
		enabled := true
		cfg.Enabled = &enabled
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return err
	}

	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Check for duplicate ID
	if mm.store.Exists(cfg.ID) {
		return fmt.Errorf("mock with ID %s already exists", cfg.ID)
	}

	// Store in mock store
	if err := mm.store.Set(cfg); err != nil {
		return err
	}

	// Register protocol-specific handlers based on mock type
	// For port-binding protocols (gRPC), this may fail if the port is in use
	if err := mm.registerHandlerLocked(cfg); err != nil {
		// Rollback: remove from store since handler registration failed
		if !mm.store.Delete(cfg.ID) {
			mm.log.Warn("failed to rollback mock store after handler registration failure",
				"id", cfg.ID)
		}
		return err
	}

	return nil
}

// Update updates an existing mock.
func (mm *MockManager) Update(id string, cfg *config.MockConfiguration) error {
	if cfg == nil {
		return errors.New("mock cannot be nil")
	}

	mm.mu.Lock()
	defer mm.mu.Unlock()

	existing := mm.store.Get(id)
	if existing == nil {
		return fmt.Errorf("mock with ID %s not found", id)
	}

	// Preserve ID and creation time
	cfg.ID = id
	cfg.CreatedAt = existing.CreatedAt
	cfg.UpdatedAt = time.Now()

	// Validate
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Unregister old handlers/servers before updating.
	// For port-binding protocols (gRPC), this stops the old server.
	// For HTTP-based protocols (WebSocket), this removes the route handler.
	// They will be re-registered with the new config by registerHandlerLocked below.
	mm.unregisterHandlerLocked(existing)

	// Store the updated config
	if err := mm.store.Set(cfg); err != nil {
		return err
	}

	// Re-register protocol handlers with the new config.
	// This starts new gRPC servers and updates HTTP-based handlers.
	// registerHandlerLocked checks the enabled flag and skips if disabled,
	// which correctly handles the toggle-to-disabled case (server stopped above,
	// not restarted here).
	if err := mm.registerHandlerLocked(cfg); err != nil {
		mm.log.Warn("failed to re-register handler after update",
			"id", id, "error", err)
		// Don't fail the update — the config is saved, the old server is stopped.
		// The user can fix the config and update again.
	}

	return nil
}

// Delete removes a mock by ID and stops any protocol-specific servers/handlers.
func (mm *MockManager) Delete(id string) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Look up the mock first to determine its type for cleanup.
	cfg := mm.store.Get(id)
	if cfg == nil {
		return fmt.Errorf("mock with ID %s not found", id)
	}

	// Clean up protocol-specific servers and handlers before removing from store.
	mm.unregisterHandlerLocked(cfg)

	if !mm.store.Delete(id) {
		return fmt.Errorf("mock with ID %s not found", id)
	}
	return nil
}

// unregisterHandlerLocked removes protocol-specific servers and handlers for a mock.
// MUST be called while holding mm.mu lock.
func (mm *MockManager) unregisterHandlerLocked(cfg *config.MockConfiguration) {
	if cfg == nil {
		return
	}

	switch cfg.Type { //nolint:exhaustive // plain HTTP mocks don't need cleanup on removal
	case mock.TypeHTTP:
		// HTTP mocks with SSE configuration have active streaming connections
		// that must be disconnected before the mock is removed or updated.
		if mm.handler != nil && cfg.HTTP != nil && cfg.HTTP.SSE != nil {
			mm.handler.DisconnectSSEByMock(cfg.ID)
		}
	case mock.TypeGRPC:
		if mm.protocolManager != nil {
			// Cancel active streams first so clients receive codes.Unavailable
			// and can reconnect with retry policies (gRPC equivalent of WS 1012).
			if srv := mm.protocolManager.GetGRPCServer(cfg.ID); srv != nil {
				n := srv.StreamTracker().CancelAll()
				if n > 0 {
					mm.log.Info("cancelled active gRPC streams", "id", cfg.ID, "count", n)
				}
			}
			if err := mm.protocolManager.StopGRPCServer(cfg.ID); err != nil {
				mm.log.Warn("failed to stop gRPC server", "id", cfg.ID, "error", err)
			}
		}
	case mock.TypeWebSocket:
		if mm.handler != nil && cfg.WebSocket != nil {
			// Disconnect active clients first so they receive close code 1012
			// (Service Restart) and reconnect with the new configuration.
			// Must happen before UnregisterWebSocketEndpoint while byEndpoint
			// still tracks the path.
			mm.handler.DisconnectWebSocketEndpoint(cfg.WebSocket.Path)
			mm.handler.UnregisterWebSocketEndpoint(cfg.WebSocket.Path)
		}
	}
}

// Get retrieves a mock by ID.
func (mm *MockManager) Get(id string) *config.MockConfiguration {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store.Get(id)
}

// List returns all mocks.
func (mm *MockManager) List() []*config.MockConfiguration {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store.List()
}

// ListByType returns mocks of a specific type.
func (mm *MockManager) ListByType(t mock.Type) []*config.MockConfiguration {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store.ListByType(t)
}

// Store returns the underlying mock store.
func (mm *MockManager) Store() storage.MockStore {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store
}

// SetStore updates the underlying mock store.
func (mm *MockManager) SetStore(store storage.MockStore) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.store = store
}

// Exists checks if a mock with the given ID exists.
func (mm *MockManager) Exists(id string) bool {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store.Exists(id)
}

// Clear removes all mocks and stops all protocol-specific servers/handlers.
func (mm *MockManager) Clear() {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Unregister all protocol handlers before clearing the store.
	for _, cfg := range mm.store.List() {
		mm.unregisterHandlerLocked(cfg)
	}

	mm.store.Clear()
}

// Count returns the number of mocks.
func (mm *MockManager) Count() int {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.store.Count()
}

// registerHandler registers the appropriate protocol handler for a mock.
// This is the public version that acquires the lock before calling registerHandlerLocked.
// Note: This is used by config_loader for loading from files where we don't want to
// fail the entire load if one mock's handler fails. Errors are logged but not returned.
func (mm *MockManager) registerHandler(cfg *config.MockConfiguration) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if err := mm.registerHandlerLocked(cfg); err != nil {
		mm.log.Warn("failed to register handler for mock", "id", cfg.ID, "name", cfg.Name, "error", err)
	}
}

// registerHandlerLocked registers the appropriate protocol handler for a mock.
// This is called after a mock is stored to wire up WebSocket, etc. handlers.
// MUST be called while holding mm.mu lock.
//
// Returns an error for protocols that require port binding (gRPC) if the
// port binding fails. For other protocols (WebSocket),
// errors are logged but not returned since they don't require exclusive ports.
func (mm *MockManager) registerHandlerLocked(cfg *config.MockConfiguration) error {
	if cfg == nil || (cfg.Enabled != nil && !*cfg.Enabled) {
		return nil
	}

	switch cfg.Type { //nolint:exhaustive // HTTP mocks are handled by the default route matcher
	case mock.TypeWebSocket:
		if cfg.WebSocket != nil {
			if err := mm.registerWebSocketMock(cfg); err != nil {
				return fmt.Errorf("failed to register WebSocket handler: %w", err)
			}
		}
	case mock.TypeGRPC:
		if cfg.GRPC != nil {
			if err := mm.registerGRPCMock(cfg); err != nil {
				return fmt.Errorf("failed to start gRPC server: %w", err)
			}
		}
		// HTTP mocks are handled by the default request handler via store lookup
	}
	return nil
}

// registerWebSocketMock registers a WebSocket mock from the unified mock struct.
func (mm *MockManager) registerWebSocketMock(m *mock.Mock) error {
	if m.WebSocket == nil {
		return errors.New("WebSocket spec is nil")
	}

	// Convert mock.WebSocketSpec to config.WebSocketEndpointConfig
	ws := m.WebSocket

	// Convert []WSMatcherConfig to []*WSMatcherConfig
	var matchers []*mock.WSMatcherConfig
	for i := range ws.Matchers {
		matchers = append(matchers, &ws.Matchers[i])
	}

	cfg := &config.WebSocketEndpointConfig{
		ID:                 m.ID,
		Name:               m.Name,
		Path:               ws.Path,
		Subprotocols:       ws.Subprotocols,
		RequireSubprotocol: ws.RequireSubprotocol,
		Matchers:           matchers,
		DefaultResponse:    ws.DefaultResponse,
		Scenario:           ws.Scenario,
		Heartbeat:          ws.Heartbeat,
		MaxMessageSize:     ws.MaxMessageSize,
		IdleTimeout:        ws.IdleTimeout,
		MaxConnections:     ws.MaxConnections,
		EchoMode:           ws.EchoMode,
	}

	return mm.handler.RegisterWebSocketEndpoint(cfg)
}

// registerGRPCMock registers a gRPC mock and starts the server.
func (mm *MockManager) registerGRPCMock(m *mock.Mock) error {
	if m.GRPC == nil {
		return errors.New("gRPC spec is nil")
	}

	if mm.protocolManager == nil {
		mm.log.Warn("gRPC mock stored but server not started (no protocol manager)",
			"name", m.Name, "port", m.GRPC.Port)
		return nil
	}

	grpcSpec := m.GRPC

	// gRPC requires proto source — either inline content or file paths
	if grpcSpec.ProtoFile == "" && len(grpcSpec.ProtoFiles) == 0 && grpcSpec.ProtoContent == "" {
		mm.log.Warn("gRPC mock has no proto source (protoFile, protoFiles, or protoContent), cannot start server",
			"name", m.Name, "port", grpcSpec.Port)
		return nil
	}

	// Convert mock.GRPCSpec to grpc.GRPCConfig
	cfg := &grpc.GRPCConfig{
		ID:           m.ID,
		Name:         m.Name,
		Port:         grpcSpec.Port,
		ProtoFile:    grpcSpec.ProtoFile,
		ProtoFiles:   grpcSpec.ProtoFiles,
		ProtoContent: grpcSpec.ProtoContent,
		ImportPaths:  grpcSpec.ImportPaths,
		Reflection:   grpcSpec.Reflection,
		Enabled:      m.Enabled == nil || *m.Enabled,
	}

	// Convert Services
	if grpcSpec.Services != nil {
		cfg.Services = make(map[string]grpc.ServiceConfig)
		for svcName, svc := range grpcSpec.Services {
			grpcSvc := grpc.ServiceConfig{
				Methods: make(map[string]grpc.MethodConfig),
			}
			for methodName, method := range svc.Methods {
				grpcSvc.Methods[methodName] = convertGRPCMethodConfig(method)
			}
			cfg.Services[svcName] = grpcSvc
		}
	}

	// Start the server via ProtocolManager
	server, err := mm.protocolManager.StartGRPCServer(cfg)
	if err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	mm.log.Info("started gRPC server", "name", m.Name, "port", server.Port())
	return nil
}

// convertGRPCMethodConfig converts a mock.MethodConfig (admin/config form) into
// the grpc.MethodConfig the server consumes. It carries ALL match variants
// through: the primary config's fields plus each entry in Variants, so that
// multiple match conditions for a single service+method survive the conversion
// (issue #30). Nested variants are flattened to a single level since only one
// level is meaningful.
func convertGRPCMethodConfig(method mock.MethodConfig) grpc.MethodConfig {
	grpcMethod := grpc.MethodConfig{
		Response:    method.Response,
		Delay:       method.Delay,
		StreamDelay: method.StreamDelay,
	}
	// Convert responses slice
	grpcMethod.Responses = append(grpcMethod.Responses, method.Responses...)
	// Convert error config
	if method.Error != nil {
		grpcMethod.Error = &grpc.GRPCErrorConfig{
			Code:    method.Error.Code,
			Message: method.Error.Message,
			Details: method.Error.Details,
		}
	}
	// Convert match config
	if method.Match != nil {
		grpcMethod.Match = &grpc.MethodMatch{
			Metadata: method.Match.Metadata,
			Request:  method.Match.Request,
		}
	}
	// Convert additional match variants in order.
	for _, variant := range method.Variants {
		converted := convertGRPCMethodConfig(variant)
		// Keep the list flat — drop any nested variants on the converted child.
		converted.Variants = nil
		grpcMethod.Variants = append(grpcMethod.Variants, converted)
	}
	return grpcMethod
}
