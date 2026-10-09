// Package file provides a file-based implementation of the store interfaces.
// Data is stored as JSON files in XDG-compliant directories.
package file

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getmockd/mockd/pkg/account"
	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/mock"
	"github.com/getmockd/mockd/pkg/store"
)

// Current data format version for migration support
const dataVersion = 1

// FileStore implements store.Store using JSON files.
type FileStore struct {
	cfg          store.Config
	mu           sync.RWMutex
	data         *storeData
	listeners    []store.ChangeListener
	listenersMu  sync.RWMutex
	dirty        atomic.Bool
	saving       atomic.Bool
	autoSave     bool
	saveDebounce time.Duration
	saveCh       chan struct{}
	closeCh      chan struct{}
	closeOnce    sync.Once
	closedCh     chan struct{} // signals when saveLoop has exited
	saveLoopOnce sync.Once     // ensures saveLoop starts exactly once
	log          *slog.Logger
}

// storeData holds all persisted data.
type storeData struct {
	Version    int                `json:"version"`
	Workspaces []*store.Workspace `json:"workspaces,omitempty"`

	// Unified mocks - all mock types in one slice
	Mocks []*mock.Mock `json:"mocks,omitempty"`

	// Stateful resource configurations (persisted across restarts)
	StatefulResources []*config.StatefulResourceConfig `json:"statefulResources,omitempty"`

	// Custom operation definitions (persisted across restarts)
	CustomOperations []*config.CustomOperationConfig `json:"customOperations,omitempty"`

	Folders     []*config.Folder         `json:"folders,omitempty"`
	Recordings  []*store.Recording       `json:"recordings,omitempty"`
	RequestLog  []*store.RequestLogEntry `json:"requestLog,omitempty"`
	Preferences *store.Preferences       `json:"preferences,omitempty"`
	LastSync    int64                    `json:"lastSync,omitempty"`

	// MockNetPack capture entities (devices and capture sessions).
	// Devices persist fully; capture sessions persist their summary
	// (times/status/counts). Temporary per-session traffic is stored
	// elsewhere  and cleared when a session ends.
	Devices         []*capture.Device         `json:"devices,omitempty"`
	CaptureSessions []*capture.CaptureSession `json:"captureSessions,omitempty"`

	// MockNetPack mock rules  and the per-device rule-set version counter
	// (keyed by app + "\x00" + did). Rules persist; the runtime Effective flag
	// is computed by the manager and never stored.
	MockRules    []*capture.MockRule `json:"mockRules,omitempty"`
	RuleVersions map[string]int      `json:"ruleVersions,omitempty"`

	// MockNetPack accounts : users and server-issued session tokens.
	// Sessions persist so a logged-in Web page survives a server restart until
	// the token expires or is revoked.
	Users        []*account.User        `json:"users,omitempty"`
	AuthSessions []*account.AuthSession `json:"authSessions,omitempty"`

	// MockNetPack QR pairing tokens . Tokens persist so a
	// just-scanned QR survives a server restart until its 10-minute TTL expires.
	PairingTokens []*account.PairingToken `json:"pairingTokens,omitempty"`

	// MockNetPack request share snapshots . Shares are independent
	// read-only copies of a single traffic entry; they persist so a share link
	// keeps its full 7-day validity across server restarts.
	Shares []*store.ShareSnapshot `json:"shares,omitempty"`

	// MockNetPack long-lived API keys . Only the
	// SHA-256 hash is persisted — the plaintext exists at creation time only
	// and is never stored. Keys survive restarts until revoked or expired.
	APIKeys []*account.APIKey `json:"apiKeys,omitempty"`
}

// New creates a new FileStore with the given configuration.
func New(cfg store.Config) *FileStore {
	if cfg.DataDir == "" {
		cfg.DataDir = store.DefaultDataDir()
	}
	fs := &FileStore{
		cfg:          cfg,
		data:         &storeData{Version: dataVersion},
		autoSave:     true,
		saveDebounce: 500 * time.Millisecond,
		saveCh:       make(chan struct{}, 1),
		closeCh:      make(chan struct{}),
		closedCh:     make(chan struct{}),
		log:          slog.Default(),
	}
	// Note: saveLoop goroutine is started in Open(), not here,
	// to ensure the data directory exists and data is loaded first.
	return fs
}

// NewWithDefaults creates a new FileStore with default configuration.
func NewWithDefaults() *FileStore {
	return New(store.DefaultConfig())
}

// saveLoop handles debounced saving to prevent excessive disk writes.
func (fs *FileStore) saveLoop() {
	defer close(fs.closedCh) // Signal that saveLoop has exited
	var timer *time.Timer
	for {
		select {
		case <-fs.saveCh:
			// Reset or create timer for debounce
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(fs.saveDebounce, func() {
				if fs.dirty.Load() && !fs.saving.Load() {
					if err := fs.doSave(); err != nil {
						fs.log.Error("failed to save store data", "error", err)
					}
				}
			})
		case <-fs.closeCh:
			if timer != nil {
				timer.Stop()
			}
			// Final save on close
			if fs.dirty.Load() {
				if err := fs.doSave(); err != nil {
					fs.log.Error("failed to save store data on close", "error", err)
				}
			}
			return
		}
	}
}

// Open initializes the store and loads data from disk.
func (fs *FileStore) Open(ctx context.Context) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Ensure directories exist with secure permissions (0700)
	dirs := []string{fs.cfg.DataDir, fs.cfg.ConfigDir, fs.cfg.CacheDir, fs.cfg.StateDir}
	for _, dir := range dirs {
		if dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
	}

	// Load data from disk
	dataFile := filepath.Join(fs.cfg.DataDir, "data.json")
	data, err := os.ReadFile(dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No data file yet, start fresh
			fs.data = &storeData{Version: dataVersion}
			fs.saveLoopOnce.Do(func() { go fs.saveLoop() })
			return nil
		}
		return err
	}

	var stored storeData
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}

	fs.data = &stored
	fs.dirty.Store(false)
	fs.saveLoopOnce.Do(func() { go fs.saveLoop() })
	return nil
}

// Close saves any pending changes and closes the store. Safe to call multiple times.
// Safe to call even if Open() was never called or Open() failed.
func (fs *FileStore) Close() error {
	// Ensure saveLoop is running so closedCh will eventually be signaled.
	// If Open() was never called, this starts saveLoop which will see closeCh
	// is closed and exit immediately.
	fs.saveLoopOnce.Do(func() { go fs.saveLoop() })
	fs.closeOnce.Do(func() {
		close(fs.closeCh)
	})
	// Wait for saveLoop to complete its final save and exit
	<-fs.closedCh
	return nil
}

// doSave performs the actual save operation with atomic write.
func (fs *FileStore) doSave() error {
	if !fs.saving.CompareAndSwap(false, true) {
		return nil // Already saving
	}
	defer fs.saving.Store(false)

	fs.mu.Lock()
	if fs.cfg.ReadOnly {
		fs.mu.Unlock()
		return store.ErrReadOnly
	}

	// Ensure version is set
	fs.data.Version = dataVersion

	data, err := json.MarshalIndent(fs.data, "", "  ")
	fs.mu.Unlock()

	if err != nil {
		return err
	}

	// Atomic write: write to temp file, fsync it, then rename, then fsync the
	// directory so the rename itself survives a crash. Without the fsyncs a
	// power loss right after the rename can leave a zero-length or missing
	// data.json (the rename is durable only after the directory entry is
	// flushed).
	dataFile := filepath.Join(fs.cfg.DataDir, "data.json")
	tmpFile := dataFile + ".tmp"

	// Ensure data directory exists (may have been removed after Open())
	if err := os.MkdirAll(fs.cfg.DataDir, 0700); err != nil {
		return err
	}

	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	if err := syncFile(tmpFile); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	if err := os.Rename(tmpFile, dataFile); err != nil {
		_ = os.Remove(tmpFile) // Clean up temp file on failure
		return err
	}
	syncDir(fs.cfg.DataDir)

	fs.dirty.Store(false)
	return nil
}

// syncFile flushes a file's contents to stable storage (fsync).
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// syncDir flushes a directory entry (best-effort: on platforms without
// directory fsync this is a no-op).
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}

// markDirty marks data as needing to be saved (thread-safe).
func (fs *FileStore) markDirty() {
	fs.dirty.Store(true)
	if fs.autoSave {
		// Non-blocking send to trigger save
		select {
		case fs.saveCh <- struct{}{}:
		default:
			// Channel full, save already pending
		}
	}
}

// ForceSave immediately saves data to disk.
func (fs *FileStore) ForceSave() error {
	fs.dirty.Store(true)
	return fs.doSave()
}

// notify sends a change event to all listeners.
func (fs *FileStore) notify(collection, operation, id string, data any) {
	fs.listenersMu.RLock()
	listeners := make([]store.ChangeListener, len(fs.listeners))
	copy(listeners, fs.listeners)
	fs.listenersMu.RUnlock()

	event := store.ChangeEvent{
		Collection: collection,
		Operation:  operation,
		ID:         id,
		Data:       data,
		Timestamp:  time.Now().UnixMilli(),
	}
	for _, l := range listeners {
		go func(listener store.ChangeListener) {
			defer func() { _ = recover() }() // Prevent listener panics from crashing store
			listener(event)
		}(l)
	}
}

// AddChangeListener adds a listener for data changes.
func (fs *FileStore) AddChangeListener(listener store.ChangeListener) {
	fs.listenersMu.Lock()
	defer fs.listenersMu.Unlock()
	fs.listeners = append(fs.listeners, listener)
}

// Workspaces returns the workspace store.
func (fs *FileStore) Workspaces() store.WorkspaceStore {
	return &workspaceStore{fs: fs}
}

// Mocks returns the mock store.
func (fs *FileStore) Mocks() store.MockStore {
	return &mockStore{fs: fs}
}

// StatefulResources returns the stateful resource store.
func (fs *FileStore) StatefulResources() store.StatefulResourceStore {
	return &statefulResourceStore{fs: fs}
}

// CustomOperations returns the custom operation store.
func (fs *FileStore) CustomOperations() store.CustomOperationStore {
	return &customOperationStore{fs: fs}
}

// Folders returns the folder store.
func (fs *FileStore) Folders() store.FolderStore {
	return &folderStore{fs: fs}
}

// Recordings returns the recordings store.
func (fs *FileStore) Recordings() store.RecordingStore {
	return &recordingStore{fs: fs}
}

// RequestLog returns the request log store.
func (fs *FileStore) RequestLog() store.RequestLogStore {
	return &requestLogStore{fs: fs}
}

// Preferences returns the preferences store.
func (fs *FileStore) Preferences() store.PreferencesStore {
	return &preferencesStore{fs: fs}
}

// Devices returns the device store (MockNetPack capture entities).
func (fs *FileStore) Devices() store.DeviceStore {
	return &deviceStore{fs: fs}
}

// CaptureSessions returns the capture session store (MockNetPack capture entities).
func (fs *FileStore) CaptureSessions() store.CaptureSessionStore {
	return &captureSessionStore{fs: fs}
}

// MockRules returns the mock rule store (MockNetPack ).
func (fs *FileStore) MockRules() store.MockRuleStore {
	return &mockRuleStore{fs: fs}
}

// Users returns the account store (MockNetPack ).
func (fs *FileStore) Users() store.UserStore {
	return &userStore{fs: fs}
}

// AuthSessions returns the auth session store (MockNetPack ).
func (fs *FileStore) AuthSessions() store.AuthSessionStore {
	return &authSessionStore{fs: fs}
}

// PairingTokens returns the QR pairing token store (MockNetPack ).
func (fs *FileStore) PairingTokens() store.PairingTokenStore {
	return &pairingTokenStore{fs: fs}
}

// Shares returns the request share snapshot store (MockNetPack ).
func (fs *FileStore) Shares() store.ShareStore {
	return &shareStore{fs: fs}
}

// APIKeys returns the long-lived API key store (MockNetPack ).
func (fs *FileStore) APIKeys() store.APIKeyStore {
	return &apiKeyStore{fs: fs}
}

// Begin starts a transaction (snapshot-based for file store).
func (fs *FileStore) Begin(ctx context.Context) (store.Transaction, error) {
	return &fileTransaction{fs: fs}, nil
}

// Sync synchronizes with remote (future CRDT support).
func (fs *FileStore) Sync(ctx context.Context) error {
	// TODO: Implement CRDT sync
	return nil
}

// LastSyncTime returns the last sync timestamp.
func (fs *FileStore) LastSyncTime() int64 {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.data.LastSync
}

// DataDir returns the data directory path.
func (fs *FileStore) DataDir() string {
	return fs.cfg.DataDir
}

// fileTransaction provides basic transaction support.
type fileTransaction struct {
	fs *FileStore
}

func (t *fileTransaction) Commit() error {
	return t.fs.ForceSave()
}

func (t *fileTransaction) Rollback() error {
	// Reload from disk to discard in-memory changes
	return t.fs.reloadFromDisk()
}

// reloadFromDisk reloads the data from the JSON file, discarding in-memory changes.
func (fs *FileStore) reloadFromDisk() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dataFile := filepath.Join(fs.cfg.DataDir, "data.json")
	data, err := os.ReadFile(dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			fs.data = &storeData{Version: dataVersion}
			fs.dirty.Store(false)
			return nil
		}
		return err
	}

	var stored storeData
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}

	fs.data = &stored
	fs.dirty.Store(false)
	return nil
}
