package store

import (
	"context"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/mock"
)

// ============================================================================
// Workspace Types - Foundation for multi-source support
// ============================================================================

// WorkspaceType represents the type of workspace backend.
type WorkspaceType string

const (
	// WorkspaceTypeLocal is a local file-based workspace (default)
	WorkspaceTypeLocal WorkspaceType = "local"
	// WorkspaceTypeGit is a git repository workspace
	WorkspaceTypeGit WorkspaceType = "git"
	// WorkspaceTypeCloud is a cloud-synced workspace
	WorkspaceTypeCloud WorkspaceType = "cloud"
	// WorkspaceTypeConfig is a read-only config file workspace
	WorkspaceTypeConfig WorkspaceType = "config"
)

// SyncStatus represents the sync state of a workspace.
type SyncStatus string

const (
	SyncStatusSynced  SyncStatus = "synced"
	SyncStatusPending SyncStatus = "pending"
	SyncStatusError   SyncStatus = "error"
	SyncStatusLocal   SyncStatus = "local" // local-only, no sync
)

// Workspace represents a collection of mocks from a specific source.
type Workspace struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Type        WorkspaceType `json:"type"`
	Description string        `json:"description,omitempty"`

	// Routing
	// BasePath is the URL prefix for this workspace's mocks on the engine.
	// Empty string means this workspace is the "root" workspace on any engine
	// that designates it as root (via Engine.RootWorkspaceID). Non-root
	// workspaces must have a non-empty BasePath (e.g., "/payment-api").
	// The default workspace starts with BasePath="" (root). New workspaces
	// auto-generate a BasePath from a slugified version of their name.
	BasePath string `json:"basePath"`

	// Backend configuration
	Path     string `json:"path,omitempty"`     // Local path or git subdir
	URL      string `json:"url,omitempty"`      // Git URL or cloud API URL
	Branch   string `json:"branch,omitempty"`   // Git branch
	ReadOnly bool   `json:"readOnly,omitempty"` // Prevent local edits

	// Sync state
	SyncStatus   SyncStatus `json:"syncStatus,omitempty"`
	LastSyncedAt int64      `json:"lastSyncedAt,omitempty"`
	AutoSync     bool       `json:"autoSync,omitempty"`

	// Metadata
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// DefaultWorkspaceID is the workspace used when no workspace is specified.
// Empty string means the default workspace.
const DefaultWorkspaceID = ""

// WorkspaceStore handles workspace persistence.
type WorkspaceStore interface {
	List(ctx context.Context) ([]*Workspace, error)
	Get(ctx context.Context, id string) (*Workspace, error)
	Create(ctx context.Context, workspace *Workspace) error
	Update(ctx context.Context, workspace *Workspace) error
	Delete(ctx context.Context, id string) error
}

// ============================================================================
// Entity Metadata - Common fields for all stored entities
// ============================================================================

// EntityMeta is an alias to config.EntityMeta for backward compatibility.
// Use config.EntityMeta directly in new code.
type EntityMeta = config.EntityMeta

// ============================================================================
// Mock Store (Unified)
// ============================================================================

// MockFilter provides filtering criteria for mock list operations.
type MockFilter struct {
	WorkspaceID string    // Filter by workspace ("" = no filter)
	Type        mock.Type // Filter by mock type ("" = all types)
	ParentID    *string   // Filter by parent folder (nil = no filter, "" = root level)
	Enabled     *bool     // Filter by enabled state (nil = no filter)
	Search      string    // Search in name/path
}

// MockStore handles persistence for all mock types in a unified manner.
type MockStore interface {
	// List returns all mocks matching the filter.
	List(ctx context.Context, filter *MockFilter) ([]*mock.Mock, error)

	// Get returns a single mock by ID.
	Get(ctx context.Context, id string) (*mock.Mock, error)

	// Create creates a new mock.
	Create(ctx context.Context, m *mock.Mock) error

	// Update updates an existing mock.
	Update(ctx context.Context, m *mock.Mock) error

	// Delete deletes a mock by ID.
	Delete(ctx context.Context, id string) error

	// DeleteByType deletes all mocks of a specific type.
	DeleteByType(ctx context.Context, mockType mock.Type) error

	// DeleteAll deletes all mocks.
	DeleteAll(ctx context.Context) error

	// Count returns the total number of mocks, optionally filtered by type.
	Count(ctx context.Context, mockType mock.Type) (int, error)

	// BulkCreate creates multiple mocks in a single operation.
	BulkCreate(ctx context.Context, mocks []*mock.Mock) error

	// BulkUpdate updates multiple mocks in a single operation.
	BulkUpdate(ctx context.Context, mocks []*mock.Mock) error
}

// StatefulResourceStore handles persistence for stateful resource configurations.
//
// Identity is (workspaceID, name): two workspaces may each register a resource
// with the same name. The workspace is taken from each config's Workspace
// field on Create. An empty workspaceID denotes the default workspace.
type StatefulResourceStore interface {
	// List returns all persisted stateful resource configs across every workspace.
	// Each entry's Workspace field identifies its bucket.
	List(ctx context.Context) ([]*config.StatefulResourceConfig, error)
	// Create persists a new stateful resource config. The Workspace field on
	// res determines its workspace bucket.
	Create(ctx context.Context, res *config.StatefulResourceConfig) error
	// Delete removes a stateful resource config from the given workspace by name.
	Delete(ctx context.Context, workspaceID, name string) error
	// DeleteAll removes every stateful resource config in the given workspace.
	// It does NOT touch other workspaces.
	DeleteAll(ctx context.Context, workspaceID string) error
}

// CustomOperationStore handles persistence for custom operation definitions.
//
// Identity is (workspaceID, name): two workspaces may each register an operation
// with the same name. The workspace is taken from each config's Workspace
// field on Create. An empty workspaceID denotes the default workspace.
type CustomOperationStore interface {
	// List returns all persisted custom operation configs across every workspace.
	// Each entry's Workspace field identifies its bucket.
	List(ctx context.Context) ([]*config.CustomOperationConfig, error)
	// Create persists a new custom operation config. The Workspace field on
	// op determines its workspace bucket.
	Create(ctx context.Context, op *config.CustomOperationConfig) error
	// Delete removes a custom operation config from the given workspace by name.
	Delete(ctx context.Context, workspaceID, name string) error
	// DeleteAll removes every custom operation config in the given workspace.
	// It does NOT touch other workspaces.
	DeleteAll(ctx context.Context, workspaceID string) error
}

// FolderFilter provides filtering criteria for folder list operations.
type FolderFilter struct {
	WorkspaceID *string // Filter by workspace (nil = no filter, "" = default workspace)
	ParentID    *string // Filter by parent folder (nil = no filter, "" = root level)
}

// FolderStore handles folder persistence.
type FolderStore interface {
	List(ctx context.Context, filter *FolderFilter) ([]*config.Folder, error)
	Get(ctx context.Context, id string) (*config.Folder, error)
	Create(ctx context.Context, folder *config.Folder) error
	Update(ctx context.Context, folder *config.Folder) error
	Delete(ctx context.Context, id string) error
	DeleteAll(ctx context.Context) error
}

// OrganizationMeta is an alias to config.OrganizationMeta for backward compatibility.
type OrganizationMeta = config.OrganizationMeta

// Recording represents a stored recording session.
type Recording struct {
	EntityMeta
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	Protocol     string `json:"protocol"` // http, grpc, websocket, etc.
	StartedAt    int64  `json:"startedAt"`
	EndedAt      int64  `json:"endedAt,omitempty"`
	RequestCount int    `json:"requestCount"`
	DataFile     string `json:"dataFile,omitempty"` // path to recording data
}

// RecordingStore handles recording persistence.
type RecordingStore interface {
	List(ctx context.Context) ([]*Recording, error)
	Get(ctx context.Context, id string) (*Recording, error)
	Create(ctx context.Context, recording *Recording) error
	Update(ctx context.Context, recording *Recording) error
	Delete(ctx context.Context, id string) error
	DeleteAll(ctx context.Context) error
}

// RequestLogEntry represents a logged request.
type RequestLogEntry struct {
	ID            string            `json:"id"`
	Protocol      string            `json:"protocol"`
	Method        string            `json:"method"`
	Path          string            `json:"path"`
	StatusCode    int               `json:"statusCode"`
	Duration      int64             `json:"duration"` // nanoseconds
	Timestamp     int64             `json:"timestamp"`
	MatchedMockID string            `json:"matchedMockId,omitempty"`
	RequestBody   string            `json:"requestBody,omitempty"`
	ResponseBody  string            `json:"responseBody,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// RequestLogStore handles request log persistence.
type RequestLogStore interface {
	List(ctx context.Context, limit, offset int) ([]*RequestLogEntry, error)
	Get(ctx context.Context, id string) (*RequestLogEntry, error)
	Append(ctx context.Context, entry *RequestLogEntry) error
	Clear(ctx context.Context) error
	Count(ctx context.Context) (int, error)
}

// Preferences represents user preferences.
type Preferences struct {
	Theme            string `json:"theme,omitempty"` // light, dark, system
	SidebarCollapsed bool   `json:"sidebarCollapsed,omitempty"`
	AutoScroll       bool   `json:"autoScroll,omitempty"`
	PollingInterval  int    `json:"pollingInterval,omitempty"` // milliseconds
	MinimizeToTray   bool   `json:"minimizeToTray,omitempty"`
	StartMinimized   bool   `json:"startMinimized,omitempty"`
	DefaultMockPort  int    `json:"defaultMockPort,omitempty"`
	DefaultAdminPort int    `json:"defaultAdminPort,omitempty"`
}

// PreferencesStore handles user preferences persistence.
type PreferencesStore interface {
	Get(ctx context.Context) (*Preferences, error)
	Set(ctx context.Context, prefs *Preferences) error
}

// ============================================================================
// MockNetPack capture entities (Device / CaptureSession)
// ============================================================================

// DeviceFilter provides filtering criteria for device list operations.
type DeviceFilter struct {
	// App filters by app dimension ("" = no filter).
	App string
}

// DeviceStore handles persistence for devices. A device is uniquely
// identified by (App, Did) — the same did may exist under different apps
// (requirement 决策 #2), so every lookup carries both dimensions.
type DeviceStore interface {
	// List returns all devices matching the filter.
	List(ctx context.Context, filter *DeviceFilter) ([]*capture.Device, error)
	// Get returns a single device by (App, Did).
	Get(ctx context.Context, app, did string) (*capture.Device, error)
	// Create adds a new device. Returns store.ErrAlreadyExists if (App, Did)
	// already exists.
	Create(ctx context.Context, d *capture.Device) error
	// Update replaces an existing device. Returns store.ErrNotFound if the
	// (App, Did) does not exist.
	Update(ctx context.Context, d *capture.Device) error
	// Delete removes a device by (App, Did).
	Delete(ctx context.Context, app, did string) error
	// Count returns the total number of devices.
	Count(ctx context.Context) (int, error)
}

// SessionFilter provides filtering criteria for capture session list operations.
type SessionFilter struct {
	// App filters by app dimension (nil = no filter).
	App *string
	// Did filters by did dimension (nil = no filter).
	Did *string
	// Status filters by session status (nil = no filter).
	Status *capture.SessionStatus
}

// CaptureSessionStore handles persistence for capture sessions.
// Session records persist across restarts (summary: times, status, counts);
// the temporary traffic inside a session is NOT stored here (M2 defines its
// own session-scoped store and clears it when the session ends).
type CaptureSessionStore interface {
	// List returns all sessions matching the filter, most recent first.
	List(ctx context.Context, filter *SessionFilter) ([]*capture.CaptureSession, error)
	// Get returns a single session by ID.
	Get(ctx context.Context, id string) (*capture.CaptureSession, error)
	// Create adds a new session. Returns store.ErrAlreadyExists if the ID
	// already exists.
	Create(ctx context.Context, s *capture.CaptureSession) error
	// Update replaces an existing session. Returns store.ErrNotFound if the
	// ID does not exist.
	Update(ctx context.Context, s *capture.CaptureSession) error
	// Delete removes a session by ID.
	Delete(ctx context.Context, id string) error
}

// MockRuleFilter filters mock rules by owning device.
type MockRuleFilter struct {
	// App filters by app dimension ("" = no filter).
	App string
	// Did filters by did dimension ("" = no filter).
	Did string
}

// MockRuleStore handles persistence for MockNetPack mock rules and the
// per-device monotonic rule-set version. Rules are persisted across restarts
// (requirement F4.5); the runtime Effective flag is computed by the manager,
// never stored here.
type MockRuleStore interface {
	// List returns all rules matching the filter (device-scoped).
	List(ctx context.Context, filter *MockRuleFilter) ([]*capture.MockRule, error)
	// Get returns a single rule by ID.
	Get(ctx context.Context, id string) (*capture.MockRule, error)
	// Create adds a new rule. Returns store.ErrAlreadyExists if the ID exists.
	Create(ctx context.Context, r *capture.MockRule) error
	// Update replaces an existing rule. Returns store.ErrNotFound if missing.
	Update(ctx context.Context, r *capture.MockRule) error
	// Delete removes a rule by ID.
	Delete(ctx context.Context, id string) error
	// GetRuleVersion returns the current rule-set version for (app, did)
	// (0 when no rules have ever been written).
	GetRuleVersion(ctx context.Context, app, did string) (int, error)
	// BumpRuleVersion increments and returns the new version for (app, did).
	BumpRuleVersion(ctx context.Context, app, did string) (int, error)
}

// ============================================================================
// MockNetPack account entities (User / AuthSession, M7.1, contract v0.6.0)
// ============================================================================

// UserStore handles persistence for accounts. Username is the unique key.
type UserStore interface {
	// GetByUsername returns a single user by username.
	GetByUsername(ctx context.Context, username string) (*account.User, error)
	// List returns all users.
	List(ctx context.Context) ([]*account.User, error)
	// Create adds a new user. Returns store.ErrAlreadyExists if the username
	// already exists.
	Create(ctx context.Context, u *account.User) error
	// Update replaces an existing user. Returns store.ErrNotFound if missing.
	Update(ctx context.Context, u *account.User) error
	// Delete removes a user by username.
	Delete(ctx context.Context, username string) error
}

// AuthSessionStore handles persistence for server-issued session tokens (M7.1).
// Sessions persist across restarts so a logged-in Web page survives a server
// restart until its token expires or is revoked.
type AuthSessionStore interface {
	// Create adds a new session token. Returns store.ErrAlreadyExists if the
	// token already exists (astronomically unlikely).
	Create(ctx context.Context, s *account.AuthSession) error
	// GetByToken returns a single session by token.
	GetByToken(ctx context.Context, token string) (*account.AuthSession, error)
	// Delete revokes a session by token. Returns store.ErrNotFound if missing.
	Delete(ctx context.Context, token string) error
	// DeleteExpired removes every session expired before now and returns the
	// number of deleted sessions (janitor).
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// PairingTokenStore handles persistence for QR pairing tokens (M9, contract
// v0.8.0). Tokens persist across restarts so a just-scanned QR survives a
// server restart until its TTL expires (10 minutes).
type PairingTokenStore interface {
	// Create adds a new pairing token. Returns store.ErrAlreadyExists if the
	// token already exists (astronomically unlikely).
	Create(ctx context.Context, p *account.PairingToken) error
	// GetByToken returns a single pairing token by token.
	GetByToken(ctx context.Context, token string) (*account.PairingToken, error)
	// RecordPairingUse appends a did to a token's paired-device list
	// (M9.3-fix, idempotent per did) so the Web can detect scan completion.
	// Unknown token is store.ErrNotFound; expiry is not enforced here
	// (validation already rejected the register request before this call).
	RecordPairingUse(ctx context.Context, token, did string, at time.Time) error
	// DeleteExpired removes every token expired before now and returns the
	// number of deleted tokens (hourly janitor; expired tokens are also
	// rejected at validation time, so this is housekeeping only).
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}
