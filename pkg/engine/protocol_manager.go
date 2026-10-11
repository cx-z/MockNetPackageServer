// Package engine provides the core mock server engine.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/getmockd/mockd/pkg/grpc"
	"github.com/getmockd/mockd/pkg/logging"
	"github.com/getmockd/mockd/pkg/protocol"
)

// ProtocolManager manages the lifecycle of all protocol handlers.
type ProtocolManager struct {
	registry      *protocol.Registry
	requestLogger RequestLogger
	log           *slog.Logger
	mu            sync.RWMutex

	// Protocol handlers
	grpcServers []*grpc.Server
}

// NewProtocolManager creates a new protocol manager.
func NewProtocolManager() *ProtocolManager {
	return &ProtocolManager{
		registry: protocol.NewRegistry(),
		log:      logging.Nop(),
	}
}

// SetLogger sets the logger for the protocol manager.
func (pm *ProtocolManager) SetLogger(log *slog.Logger) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if log != nil {
		pm.log = log
	} else {
		pm.log = logging.Nop()
	}
}

// SetRequestLogger sets the request logger for all protocol handlers.
func (pm *ProtocolManager) SetRequestLogger(logger RequestLogger) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.requestLogger = logger
}

// Registry returns the protocol handler registry.
func (pm *ProtocolManager) Registry() *protocol.Registry {
	return pm.registry
}

// StartAll starts all configured protocol handlers.
// handler is passed so gRPC and WebSocket handlers can be registered.
func (pm *ProtocolManager) StartAll(ctx context.Context, cfg *ProtocolConfig, handler *Handler) error {
	// Start gRPC
	if err := pm.startGRPC(ctx, cfg); err != nil {
		return fmt.Errorf("failed to start gRPC: %w", err)
	}
	return nil
}

// ProtocolConfig contains protocol-specific configurations.
// This is extracted from config.ServerConfiguration for the ProtocolManager.
type ProtocolConfig struct {
	GRPC []*grpc.GRPCConfig
}

// StopAll stops all protocol handlers.
func (pm *ProtocolManager) StopAll(ctx context.Context, timeout time.Duration) []error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var errs []error

	// Stop gRPC servers
	for _, server := range pm.grpcServers {
		if err := server.Stop(ctx, timeout); err != nil {
			errs = append(errs, fmt.Errorf("gRPC server stop: %w", err))
		}
	}
	pm.grpcServers = nil

	return errs
}

// startGRPC starts all configured gRPC servers.
func (pm *ProtocolManager) startGRPC(ctx context.Context, cfg *ProtocolConfig) error {
	if cfg.GRPC == nil {
		return nil
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, grpcCfg := range cfg.GRPC {
		if grpcCfg == nil || !grpcCfg.Enabled {
			continue
		}

		// Parse proto — prefer inline content (works when engine is remote),
		// fall back to file paths on the local filesystem.
		var (
			schema   *grpc.ProtoSchema
			parseErr error
		)
		if grpcCfg.ProtoContent != "" {
			schema, parseErr = grpc.ParseProtoContent(grpcCfg.ProtoContent)
		} else {
			protoFiles := grpcCfg.GetProtoFiles()
			schema, parseErr = grpc.ParseProtoFiles(protoFiles, grpcCfg.ImportPaths)
		}
		if parseErr != nil {
			return fmt.Errorf("failed to parse proto for gRPC server on port %d: %w", grpcCfg.Port, parseErr)
		}

		// Create gRPC server
		server, err := grpc.NewServer(grpcCfg, schema)
		if err != nil {
			return fmt.Errorf("failed to create gRPC server on port %d: %w", grpcCfg.Port, err)
		}

		// Set request logger for unified logging
		if pm.requestLogger != nil {
			server.SetRequestLogger(pm.requestLogger)
		}

		// Start the server
		if err := server.Start(ctx); err != nil {
			return fmt.Errorf("failed to start gRPC server on port %d: %w", grpcCfg.Port, err)
		}

		// Register with protocol registry for unified handler management
		if err := pm.registry.Register(server); err != nil {
			pm.log.Warn("failed to register gRPC server with protocol registry", "port", grpcCfg.Port, "error", err)
		}

		pm.grpcServers = append(pm.grpcServers, server)
	}

	return nil
}

// GRPCServers returns all running gRPC servers.
func (pm *ProtocolManager) GRPCServers() []*grpc.Server {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]*grpc.Server, len(pm.grpcServers))
	copy(result, pm.grpcServers)
	return result
}

// GetGRPCServer returns a gRPC server by ID.
func (pm *ProtocolManager) GetGRPCServer(id string) *grpc.Server {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, srv := range pm.grpcServers {
		if srv != nil && srv.ID() == id {
			return srv
		}
	}
	return nil
}

// StartGRPCServer dynamically starts a gRPC server with the given configuration.
// Returns the server instance or an error if startup fails.
func (pm *ProtocolManager) StartGRPCServer(cfg *grpc.GRPCConfig) (*grpc.Server, error) {
	if cfg == nil {
		return nil, errors.New("gRPC config cannot be nil")
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Check if a server with this ID is already running
	for _, srv := range pm.grpcServers {
		if srv != nil && srv.ID() == cfg.ID {
			if srv.IsRunning() {
				return srv, nil // Already running
			}
			// Remove the stopped server from the list
			pm.removeGRPCServerLocked(cfg.ID)
			break
		}
	}

	// Check for port conflicts
	for _, srv := range pm.grpcServers {
		if srv != nil && srv.IsRunning() && srv.Config().Port == cfg.Port {
			return nil, fmt.Errorf("port %d is already in use by gRPC server %s", cfg.Port, srv.ID())
		}
	}

	// Parse proto — prefer inline content (works when engine is remote),
	// fall back to file paths on the local filesystem.
	var (
		schema   *grpc.ProtoSchema
		parseErr error
	)
	if cfg.ProtoContent != "" {
		schema, parseErr = grpc.ParseProtoContent(cfg.ProtoContent)
	} else {
		protoFiles := cfg.GetProtoFiles()
		if len(protoFiles) == 0 {
			return nil, errors.New("no proto files or inline content specified")
		}
		schema, parseErr = grpc.ParseProtoFiles(protoFiles, cfg.ImportPaths)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse proto: %w", parseErr)
	}

	// Create gRPC server
	server, err := grpc.NewServer(cfg, schema)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC server: %w", err)
	}

	// Set request logger for unified logging
	if pm.requestLogger != nil {
		server.SetRequestLogger(pm.requestLogger)
	}

	// Start the server. If the port was just released by a stopped server,
	// the OS may need a moment to fully free it — retry once after a short delay.
	if err := server.Start(context.Background()); err != nil {
		if isAddrInUse(err) {
			time.Sleep(150 * time.Millisecond)
			if retryErr := server.Start(context.Background()); retryErr != nil {
				return nil, fmt.Errorf("failed to start gRPC server on port %d (after retry): %w", cfg.Port, retryErr)
			}
		} else {
			return nil, fmt.Errorf("failed to start gRPC server on port %d: %w", cfg.Port, err)
		}
	}

	// Register with protocol registry for unified handler management
	if err := pm.registry.Register(server); err != nil {
		pm.log.Warn("failed to register gRPC server with protocol registry", "port", cfg.Port, "error", err)
	}

	pm.grpcServers = append(pm.grpcServers, server)
	return server, nil
}

// StopGRPCServer stops a running gRPC server by ID.
func (pm *ProtocolManager) StopGRPCServer(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for i, srv := range pm.grpcServers {
		if srv != nil && srv.ID() == id {
			if err := srv.Stop(context.Background(), 5*time.Second); err != nil {
				return fmt.Errorf("failed to stop gRPC server %s: %w", id, err)
			}
			// Unregister from protocol registry
			if err := pm.registry.Unregister(id); err != nil {
				pm.log.Warn("failed to unregister gRPC server from protocol registry", "id", id, "error", err)
			}
			// Remove from slice
			pm.grpcServers = append(pm.grpcServers[:i], pm.grpcServers[i+1:]...)
			return nil
		}
	}

	return nil // Server not found, nothing to stop
}

// removeGRPCServerLocked removes a gRPC server from the list (caller must hold the lock).
func (pm *ProtocolManager) removeGRPCServerLocked(id string) {
	for i, srv := range pm.grpcServers {
		if srv != nil && srv.ID() == id {
			pm.grpcServers = append(pm.grpcServers[:i], pm.grpcServers[i+1:]...)
			return
		}
	}
}

// isAddrInUse reports whether err is caused by a port already being in use.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *syscall.Errno
		if errors.As(opErr.Err, &sysErr) {
			return *sysErr == syscall.EADDRINUSE
		}
	}
	return false
}
