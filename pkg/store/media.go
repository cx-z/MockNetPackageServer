package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getmockd/mockd/pkg/capture"
)

// File: media.go
// Mock rule binary body blobs (M15 方案 D 阶段二：大图字节治理).
// ============================================================================
// Media store
// ============================================================================
//
// 一条规则一个文件：<dataDir>/media/<ruleID>.bin. The blob is fetched once at
// rule creation ("Mock 此请求" from a captured big-image response) and served
// to the SDK on demand by GET /devices/{app}/{did}/mock-rules/{ruleId}/media.
// The rule row itself only carries a MediaRef (sha256/size/contentType), so a
// 7 MB image never travels through the rule snapshot pull.
//
// Concurrency: Put/Delete run under mediaMu (they also maintain the running
// total). Get does not take the lock — a blob is written to a temp file and
// renamed, so readers either see the old complete file or the new one.

const (
	// MaxMediaBytes caps a single blob (8 MB): large enough for a full-
	// size poster/screenshot, small enough that N rules cannot exhaust disk.
	MaxMediaBytes int64 = 8 << 20
	// DefaultMediaDirBytes caps the whole media directory (500 MB). Reaching
	// it rejects new blobs instead of evicting live rules (an evicted blob
	// would silently break an existing mock rule).
	DefaultMediaDirBytes int64 = 500 << 20
)

var (
	// ErrMediaTooLarge means the fetched body exceeds MaxMediaBytes.
	ErrMediaTooLarge = errors.New("media body exceeds the per-blob limit")
	// ErrMediaQuotaExceeded means the media directory hit its total cap.
	ErrMediaQuotaExceeded = errors.New("media directory quota exceeded")
	// ErrMediaNotFound means no blob exists for the given rule.
	ErrMediaNotFound = errors.New("media blob not found")
	// ErrMediaUnavailable means a media rule was requested but the manager has
	// no media store wired (server misconfiguration).
	ErrMediaUnavailable = errors.New("media store not configured")
)

// MediaStore persists mock-rule binary bodies on disk.
type MediaStore struct {
	dir string
	// limit is the total-bytes cap for the whole directory (bytes).
	limit int64

	mu       sync.Mutex
	total    int64
	totalSet bool // total scanned from disk at least once
}

// NewMediaStore creates a store rooted at dir. The directory is created lazily
// on the first Put; Get on a missing dir behaves like an empty store.
func NewMediaStore(dir string) *MediaStore {
	return &MediaStore{dir: dir, limit: DefaultMediaDirBytes}
}

// SetLimit overrides the directory cap (tests / future config item).
func (s *MediaStore) SetLimit(bytes int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.limit = bytes
	s.mu.Unlock()
}

// Dir reports the backing directory ("" when the store is disabled).
func (s *MediaStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// blobPath is the on-disk location of a rule's blob.
func (s *MediaStore) blobPath(ruleID string) string {
	return filepath.Join(s.dir, ruleID+".bin")
}

// Put stores data for ruleID and returns its MediaRef. It rejects bodies above
// MaxMediaBytes and writes that would push the directory over its cap.
// The write is atomic (temp file + rename), so a concurrent Get never observes
// a half-written blob.
func (s *MediaStore) Put(ruleID string, data []byte, contentType string) (*capture.MediaRef, error) {
	if s == nil || s.dir == "" {
		return nil, ErrMediaNotFound
	}
	if ruleID == "" {
		return nil, errors.New("media: empty rule id")
	}
	size := int64(len(data))
	if size == 0 {
		return nil, ErrMediaNotFound
	}
	if size > MaxMediaBytes {
		return nil, ErrMediaTooLarge
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("media: create dir: %w", err)
	}

	sum := sha256.Sum256(data)
	ref := &capture.MediaRef{
		SHA256:      hex.EncodeToString(sum[:]),
		Size:        size,
		ContentType: contentType,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanTotalLocked()
	existing := s.fileSizeLocked(ruleID)
	if s.limit > 0 && s.total-existing+size > s.limit {
		return nil, ErrMediaQuotaExceeded
	}

	tmp, err := os.CreateTemp(s.dir, ruleID+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("media: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("media: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("media: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.blobPath(ruleID)); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("media: rename: %w", err)
	}
	s.total = s.total - existing + size
	return ref, nil
}

// Get returns the blob bytes for ruleID. ErrMediaNotFound when absent.
func (s *MediaStore) Get(ruleID string) ([]byte, error) {
	if s == nil || s.dir == "" || ruleID == "" {
		return nil, ErrMediaNotFound
	}
	data, err := os.ReadFile(s.blobPath(ruleID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMediaNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Delete removes the blob of ruleID (no-op when absent). Callers delete the
// blob whenever the owning rule is deleted, purged, or edited away from the
// media path.
func (s *MediaStore) Delete(ruleID string) {
	if s == nil || s.dir == "" || ruleID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanTotalLocked()
	size := s.fileSizeLocked(ruleID)
	if err := os.Remove(s.blobPath(ruleID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	if size > 0 {
		s.total -= size
		if s.total < 0 {
			s.total = 0
		}
	}
}

// SweepOrphans removes blobs whose ruleID is not in keep. Called at startup:
// a crash between Put and Mutate (or a rule deleted while the process was
// down) leaves files no rule points at. Best-effort — errors are ignored so a
// partially readable directory cannot block startup.
func (s *MediaStore) SweepOrphans(keep map[string]bool) {
	if s == nil || s.dir == "" {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".bin") {
			// 遗留的临时文件（写一半崩溃）一并清理。
			if strings.HasSuffix(name, ".tmp") {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
			continue
		}
		ruleID := strings.TrimSuffix(name, ".bin")
		if keep[ruleID] {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, name))
	}
	s.totalSet = false
}

// TotalBytes reports the directory size (scanning it on first use).
func (s *MediaStore) TotalBytes() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanTotalLocked()
	return s.total
}

// scanTotalLocked computes the directory total once (caller holds mu).
func (s *MediaStore) scanTotalLocked() {
	if s.totalSet {
		return
	}
	s.totalSet = true
	s.total = 0
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".bin") {
			s.total += info.Size()
		}
	}
}

// fileSizeLocked returns the current blob size for ruleID (0 when absent).
func (s *MediaStore) fileSizeLocked(ruleID string) int64 {
	if info, err := os.Stat(s.blobPath(ruleID)); err == nil {
		return info.Size()
	}
	return 0
}
