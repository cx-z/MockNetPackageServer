// Package admin provides a REST API for managing mock configurations.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getmockd/mockd/pkg/admin/engineclient"
	"github.com/getmockd/mockd/pkg/logging"
	"github.com/getmockd/mockd/pkg/metrics"
	"github.com/getmockd/mockd/pkg/ratelimit"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/getmockd/mockd/pkg/store/file"
	"github.com/getmockd/mockd/pkg/tracing"
	"github.com/getmockd/mockd/pkg/workspace"
)

// EngineHeartbeatTimeout is the duration after which an engine is marked offline
// if no heartbeat is received.
const EngineHeartbeatTimeout = 30 * time.Second

// DefaultRateLimit is the default requests per second limit for the admin API.
const DefaultRateLimit float64 = 100

// DefaultBurstSize is the default burst size for the admin API.
const DefaultBurstSize int = 200

// API exposes a REST API for managing mock configurations.
type API struct {
	// localEngine is the HTTP client for communicating with the local engine.
	// Stored as atomic.Pointer to prevent data races between SetLocalEngine /
	// handleRegisterEngine (writers) and handler goroutines (readers).
	localEngine atomic.Pointer[engineclient.Client]

	proxyManager           *ProxyManager
	streamRecordingManager *StreamRecordingManager
	mqttRecordingManager   *MQTTRecordingManager
	soapRecordingManager   *SOAPRecordingManager
	workspaceStore         *store.WorkspaceFileStore
	engineRegistry         *store.EngineRegistry
	captureManager         *store.CaptureManager
	captureConfig          store.CaptureConfig
	mockNetPackWebDir      string // MockNetPack web UI 静态目录（默认 web/mocknetpack）
	workspaceManager       workspace.Manager
	dataStore              *file.FileStore // Persistent store for mocks and folders
	httpServer             *http.Server
	port                   int
	startTime              time.Time
	ctx                    context.Context
	cancel                 context.CancelFunc
	log                    atomic.Pointer[slog.Logger]

	// MockNetPack account stores : users and server-issued session tokens.
	users        store.UserStore
	authSessions store.AuthSessionStore

	// MockNetPack long-lived API keys : machine credentials for scripts /
	// Agent tooling, authenticated through the same Bearer header.
	apiKeys store.APIKeyStore

	// loginThrottle (4.16): failure-based lockout for the login endpoint,
	// per-username and per-IP (loopback exempt).
	loginThrottle *loginThrottle

	// engineSyncMu prevents concurrent admin-store-to-engine syncs (legacy global mutex).
	// Used as fallback when per-engine mutex is not applicable.
	engineSyncMu sync.Mutex

	// perEngineSync manages per-engine sync mutexes so concurrent full-syncs
	// to different engines don't block each other.
	perEngineSync *perEngineSyncMu

	// Token management for engine authentication
	registrationTokens map[string]storedToken // token -> storedToken
	engineTokens       map[string]storedToken // engineID -> storedToken
	tokenMu            sync.RWMutex

	// Token expiration configuration (can be overridden)
	registrationTokenExpiration time.Duration
	engineTokenExpiration       time.Duration

	// API key authentication
	apiKeyAuth   *apiKeyAuth
	apiKeyConfig APIKeyConfig

	// Rate limiter for API protection
	rateLimiter *ratelimit.PerIPLimiter

	// CORS configuration
	corsConfig CORSConfig

	// Metrics registry for Prometheus metrics
	metricsRegistry *metrics.Registry

	// Tracer for distributed tracing (optional)
	tracer *tracing.Tracer

	// Custom data directory (for test isolation)
	dataDir string

	// Version string for status endpoint
	version string

	// AllowLocalhostBypass allows unauthenticated access from localhost (dev mode only)
	// Default is false - authentication is always required
	allowLocalhostBypass bool

	// Tunnel state for local engine
	localTunnel *store.TunnelConfig
	tunnelMu    sync.RWMutex

	// ready indicates that the server has completed initialization
	// (config loaded, engine healthy, ready to serve traffic).
	ready atomic.Bool

	// noPersist (4.14): with --no-persist, a data-store Open failure degrades
	// to an in-memory-only run instead of failing startup.
	noPersist bool
	// storeOpenErr records a data-store Open failure so Start() can fail
	// startup instead of silently running without persistence.
	storeOpenErr error
}

// NewAPI creates a new API.
func NewAPI(port int, opts ...Option) *API {
	// Create context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize metrics registry
	metricsRegistry := metrics.Init()

	api := &API{
		proxyManager:                NewProxyManager(),
		streamRecordingManager:      NewStreamRecordingManager(),
		mqttRecordingManager:        NewMQTTRecordingManager(),
		soapRecordingManager:        NewSOAPRecordingManager(),
		engineRegistry:              store.NewEngineRegistry(),
		perEngineSync:               newPerEngineSyncMu(),
		port:                        port,
		startTime:                   time.Now(),
		ctx:                         ctx,
		cancel:                      cancel,
		registrationTokens:          make(map[string]storedToken),
		engineTokens:                make(map[string]storedToken),
		registrationTokenExpiration: RegistrationTokenExpiration,
		engineTokenExpiration:       EngineTokenExpiration,
		apiKeyConfig:                DefaultAPIKeyConfig(),
		metricsRegistry:             metricsRegistry,
		loginThrottle:               newLoginThrottle(LoginFailureLimit, LoginFailureWindow),
	}

	// Store default nop logger (can be replaced with SetLogger before Start)
	api.log.Store(logging.Nop())

	// Apply options first so dataDir can be set
	for _, opt := range opts {
		opt(api)
	}

	// Initialize the workspace file store (after options to use dataDir if set)
	wsStore := store.NewWorkspaceFileStore(api.dataDir)
	if err := wsStore.Open(context.Background()); err != nil {
		// Log but don't fail - workspace features will be limited
		api.logger().Warn("failed to initialize workspace store", "error", err)
	}
	api.workspaceStore = wsStore

	// Initialize the data store for mocks and folders (after options to use dataDir if set)
	var dataStore *file.FileStore
	if api.dataDir != "" {
		cfg := store.DefaultConfig()
		cfg.DataDir = api.dataDir
		dataStore = file.New(cfg)
	} else {
		dataStore = file.NewWithDefaults()
	}
	if err := dataStore.Open(context.Background()); err != nil {
		if api.noPersist {
			// 4.14: explicit --no-persist — degrade to an in-memory-only run.
			api.logger().Warn("failed to initialize data store (--no-persist: continuing without persistence)", "error", err)
		} else {
			// 4.14: persistence is the default contract. A store that failed to
			// open runs without its save loop, so every write silently vanishes
			// on restart — fail startup instead of pretending to be healthy.
			api.storeOpenErr = fmt.Errorf("open data store: %w", err)
			api.logger().Error("failed to initialize data store; startup will fail (use --no-persist to run in-memory only)", "error", err)
		}
	}
	api.dataStore = dataStore

	// Initialize the MockNetPack capture manager (devices / capture sessions /
	// viewer leases / mock rules / QR pairing tokens / share snapshots). Uses
	// defaults unless overridden via WithCaptureConfig.
	api.captureManager = store.NewCaptureManager(
		dataStore.Devices(),
		dataStore.CaptureSessions(),
		dataStore.MockRules(),
		dataStore.PairingTokens(),
		dataStore.Shares(),
		api.captureConfig,
	)
	//  存储水位监控：把水位检查指向持久化 data.json，启动时及每小时
	// 输出体积日志（超 500MB WARN，不阻断）。
	api.captureManager.SetDataFilePath(filepath.Join(dataStore.DataDir(), "data.json"))

	// Initialize the MockNetPack account stores : users and session
	// tokens share the same persistent FileStore. Long-lived API keys 
	// persist the same way.
	api.users = dataStore.Users()
	api.authSessions = dataStore.AuthSessions()
	api.apiKeys = dataStore.APIKeys()

	// Initialize rate limiter with defaults if not provided via options
	if api.rateLimiter == nil {
		api.rateLimiter = ratelimit.NewPerIPLimiter(ratelimit.PerIPConfig{
			Rate:  DefaultRateLimit,
			Burst: DefaultBurstSize,
		})
	}

	// Initialize CORS config with defaults if not provided via options
	// Check if corsConfig is zero value (no AllowedOrigins set)
	if len(api.corsConfig.AllowedOrigins) == 0 && len(api.corsConfig.AllowedMethods) == 0 {
		api.corsConfig = DefaultCORSConfig()
	}

	// Initialize API key authentication.
	// The logFn closure reads the logger dynamically via api.logger() so
	// that it picks up any logger set later via SetLogger (C3 fix).
	logFn := func(msg string, args ...any) {
		api.logger().Info(msg, args...)
	}
	apiKeyAuth, err := newAPIKeyAuth(api.apiKeyConfig, logFn)
	if err != nil {
		api.logger().Error("failed to initialize API key auth", "error", err)
		// Continue without API key auth - will be insecure but functional
	}
	api.apiKeyAuth = apiKeyAuth

	// Initialize stream recording manager with data directory
	if err := api.streamRecordingManager.Initialize(api.dataDir); err != nil {
		api.logger().Warn("failed to initialize stream recording manager", "error", err)
		// Continue without stream recording - feature will be unavailable
	}

	mux := http.NewServeMux()
	api.registerRoutes(mux)
	api.registerDashboard(mux) // no-op unless built with -tags dashboard
	api.registerMockNetPackWeb(mux)

	api.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      api.withMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return api
}

// InitializeStreamRecordings initializes the stream recording manager with the given data directory.
func (a *API) InitializeStreamRecordings(dataDir string) error {
	return a.streamRecordingManager.Initialize(dataDir)
}

// StreamRecordingManager returns the stream recording manager.
func (a *API) StreamRecordingManager() *StreamRecordingManager {
	return a.streamRecordingManager
}

// MQTTRecordingManager returns the MQTT recording manager.
func (a *API) MQTTRecordingManager() *MQTTRecordingManager {
	return a.mqttRecordingManager
}

// SOAPRecordingManager returns the SOAP recording manager.
func (a *API) SOAPRecordingManager() *SOAPRecordingManager {
	return a.soapRecordingManager
}

// EngineRegistry returns the engine registry.
func (a *API) EngineRegistry() *store.EngineRegistry {
	return a.engineRegistry
}

// WorkspaceManager returns the workspace manager for multi-workspace serving.
// Returns nil if no workspace manager was configured via WithWorkspaceManager.
func (a *API) WorkspaceManager() workspace.Manager {
	return a.workspaceManager
}

// LocalEngine returns the local engine HTTP client.
// Returns nil if no local engine is configured.
// Safe to call from any goroutine.
func (a *API) LocalEngine() *engineclient.Client {
	return a.localEngine.Load()
}

// SetLocalEngine atomically sets the local engine client after the admin has
// started. This allows connecting an engine that was started after the admin.
// Safe to call concurrently with handler goroutines that read via localEngine.Load().
func (a *API) SetLocalEngine(client *engineclient.Client) {
	a.localEngine.Store(client)
}

// MetricsRegistry returns the metrics registry for Prometheus metrics.
func (a *API) MetricsRegistry() *metrics.Registry {
	return a.metricsRegistry
}

// HasLocalEngine returns true if a local engine is configured.
// Safe to call from any goroutine.
func (a *API) HasLocalEngine() bool {
	return a.localEngine.Load() != nil
}

// maxRequestBodySize is the default maximum request body size for admin API handlers (10MB).
// This must be at least as large as the limits used by individual handlers
// (e.g., config import and bulk create both allow up to 10MB) because the
// middleware applies this limit before handlers run, and wrapping a
// MaxBytesReader with a larger MaxBytesReader does not increase the limit.
const maxRequestBodySize = 10 << 20

// withMiddleware wraps the handler with rate limiting, logging, security headers, CORS, API key auth, and tracing middleware.
// Middleware order (outermost to innermost): Tracing -> Security Headers -> CORS -> API Key Auth -> Rate Limiting -> Workspace Normalization -> Body Limit -> Handler
func (a *API) withMiddleware(handler http.Handler) http.Handler {
	// Body size limit (innermost — protects all handlers from oversized request bodies)
	bodyCapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		handler.ServeHTTP(w, r)
	})

	// Apply rate limiting
	rateLimited := ratelimit.Middleware(a.rateLimiter, ratelimit.WithTextResponse())(bodyCapped)

	// API key authentication wraps rate limiting
	authenticated := rateLimited
	if a.apiKeyAuth != nil {
		authenticated = a.apiKeyAuth.middleware(rateLimited)
	}

	// CORS middleware wraps API key auth
	corsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Always set Vary header to indicate origin-dependent response
		w.Header().Add("Vary", "Origin")

		// Get the appropriate Allow-Origin value based on config
		allowOrigin := a.corsConfig.getAllowOriginValue(origin)
		if allowOrigin == "" {
			// Origin not allowed - process request but browser will block response
			// For preflight, we should return early without CORS headers
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			authenticated.ServeHTTP(w, r)
			return
		}

		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", a.corsConfig.getMethods())
		w.Header().Set("Access-Control-Allow-Headers", a.corsConfig.getHeaders())
		w.Header().Set("Access-Control-Max-Age", a.corsConfig.getMaxAge())

		// Set credentials header if enabled
		if a.corsConfig.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		// Handle preflight requests (don't rate limit OPTIONS)
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		authenticated.ServeHTTP(w, r)
	})

	// Security headers middleware wraps CORS
	securityHandler := SecurityHeadersMiddleware(corsHandler)

	//  debug: access log middleware (always on, regardless of tracer)
	accessLogged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &adminStatusCapturingResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		securityHandler.ServeHTTP(wrapped, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"remote", r.RemoteAddr,
		)
	})

	// Tracing middleware (outermost, captures full request lifecycle)
	// Only applied if a tracer is configured
	if a.tracer != nil {
		return a.tracingMiddleware(accessLogged)
	}

	return accessLogged
}

// skipTracingPaths contains paths that should not create traces.
// These are typically health checks and metrics endpoints.
var skipTracingPaths = map[string]bool{
	"/metrics":  true,
	"/health":   true,
	"/healthz":  true,
	"/ready":    true,
	"/readyz":   true,
	"/livez":    true,
	"/_/health": true,
}

// tracingMiddleware wraps a handler with distributed tracing support.
func (a *API) tracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip tracing for health/metrics endpoints to avoid noise
		if skipTracingPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Extract trace context from incoming request headers
		ctx := tracing.Extract(r.Context(), r.Header)

		// Create span name like "HTTP GET /path"
		spanName := fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path)

		// Start a new span
		ctx, span := a.tracer.Start(ctx, spanName)
		defer span.End()

		// Set HTTP request attributes
		span.SetAttribute("http.method", r.Method)
		span.SetAttribute("http.url", r.URL.String())
		span.SetAttribute("http.target", r.URL.Path)
		span.SetAttribute("http.host", r.Host)

		// Wrap response writer to capture status code
		wrapped := &adminStatusCapturingResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		// Update request context with span
		r = r.WithContext(ctx)

		// Call the next handler
		next.ServeHTTP(wrapped, r)

		// Set response attributes
		span.SetAttribute("http.status_code", strconv.Itoa(wrapped.statusCode))

		// Set span status based on HTTP status code
		switch {
		case wrapped.statusCode >= 500:
			span.SetStatus(tracing.StatusError, fmt.Sprintf("HTTP server error: %d", wrapped.statusCode))
		case wrapped.statusCode >= 400:
			span.SetStatus(tracing.StatusError, fmt.Sprintf("HTTP client error: %d", wrapped.statusCode))
		default:
			span.SetStatus(tracing.StatusOK, "")
		}
	})
}

// adminStatusCapturingResponseWriter wraps http.ResponseWriter to capture the status code.
type adminStatusCapturingResponseWriter struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

// WriteHeader captures the status code before writing the header.
func (w *adminStatusCapturingResponseWriter) WriteHeader(code int) {
	if !w.headerWritten {
		w.statusCode = code
		w.headerWritten = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write captures status code if not already written (implicit 200 OK).
func (w *adminStatusCapturingResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWritten {
		w.statusCode = http.StatusOK
		w.headerWritten = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController support.
func (w *adminStatusCapturingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Tracer returns the tracer, if configured.
func (a *API) Tracer() *tracing.Tracer {
	return a.tracer
}

// Start starts the admin API server.
func (a *API) Start() error {
	a.startTime = time.Now()

	// 4.14: a failed data-store open means all persistence is silently dead
	// (no save loop). Refuse to start — unless the caller explicitly opted
	// into an in-memory-only run via --no-persist.
	if a.storeOpenErr != nil {
		return fmt.Errorf("persistent data store unavailable: %w (restart with --no-persist to run without persistence)", a.storeOpenErr)
	}

	// Start the engine health check background goroutine
	a.engineRegistry.StartHealthCheck(a.ctx, EngineHeartbeatTimeout)

	//  存储水位：启动时输出一行 data.json 体积（之后由 hourly janitor
	// 每小时输出，超 500MB WARN 一次）。
	a.captureManager.LogDataFileSize()

	// Start the MockNetPack capture health check (device heartbeat timeout +
	// viewer lease garbage collection)
	a.captureManager.StartHealthCheck(a.ctx)

	// Start the token cleanup background goroutine
	go a.startTokenCleanup(a.ctx)

	a.logger().Info("starting admin API", "port", a.port)

	// Use synchronous Listen to catch port-in-use errors immediately
	// rather than losing them inside a goroutine.
	ln, err := net.Listen("tcp", a.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on admin port %d: %w", a.port, err)
	}
	go func() {
		if err := a.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger().Error("admin API error", "error", err)
		}
	}()
	return nil
}

// logger returns the current logger from the atomic pointer.
// This is safe to call from any goroutine concurrently with SetLogger.
func (a *API) logger() *slog.Logger {
	return a.log.Load()
}

// SetLogger atomically sets the operational logger for the admin API and
// propagates it to all manager subsystems via their own lock-protected
// SetLogger methods. Safe to call concurrently with handler goroutines
// that read the logger via the logger() accessor.
func (a *API) SetLogger(log *slog.Logger) {
	if log == nil {
		log = logging.Nop()
	}
	a.log.Store(log)

	// Fan out to managers via their lock-protected SetLogger methods so
	// that concurrent handler goroutines never observe a torn pointer write.
	a.proxyManager.SetLogger(log.With("component", "proxy"))
	a.streamRecordingManager.SetLogger(log.With("component", "stream-recording"))
	a.mqttRecordingManager.SetLogger(log.With("component", "mqtt-recording"))
	a.soapRecordingManager.SetLogger(log.With("component", "soap-recording"))
	a.captureManager.SetLogger(log.With("component", "capture"))
}

// Stop gracefully shuts down the admin API server.
func (a *API) Stop() error {
	// 4.15: drain the HTTP server FIRST. In-flight handlers (e.g. UploadTraffic
	// during a capture) still write through the data store; closing the store
	// before they finish would strand those writes (save loop already gone)
	// and race the final save. Shutdown waits up to 5s for in-flight requests.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := a.httpServer.Shutdown(ctx)

	// Stop background goroutines AFTER the server is drained — nothing new can
	// arrive anymore, and their final state must land in the store below.
	a.cancel()
	a.engineRegistry.Stop()
	a.captureManager.Stop()

	// Stop the rate limiter cleanup goroutine
	if a.rateLimiter != nil {
		a.rateLimiter.Stop()
	}

	// Stop all workspace servers
	if a.workspaceManager != nil {
		if err := a.workspaceManager.StopAll(); err != nil {
			a.logger().Warn("error stopping workspace servers", "error", err)
		}
	}

	// Close the data store LAST so the final save includes everything the
	// drained requests wrote.
	if a.dataStore != nil {
		if err := a.dataStore.Close(); err != nil {
			a.logger().Warn("error closing data store", "error", err)
		}
	}

	return shutdownErr
}

// SetReady marks the API as ready to serve traffic.
// Called after initial config import completes.
func (a *API) SetReady() {
	a.ready.Store(true)
}

// IsReady returns true if the API has completed initialization.
func (a *API) IsReady() bool {
	return a.ready.Load()
}

// Uptime returns the API uptime in seconds.
func (a *API) Uptime() int {
	return int(time.Since(a.startTime).Seconds())
}
