package file

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// trafficStore implements store.TrafficStore as one JSON file per capture
// session under <dataDir>/traffic/<sessionID>.json (跨重启保留请求存档).
// Each file holds the session's full traffic slice ([]*capture.TrafficEntry);
// the per-session rolling-window cap (DefaultMaxSessionTrafficEntries) bounds
// a single file, and the 48h ended-session retention + janitor bound the
// directory. Writes are atomic (tmp + fsync + rename), so a crash leaves
// either the old or the new complete file, never a partial one.
//
// Concurrency: the CaptureManager serializes all mutations of one session's
// slice under trafficMu, so this store needs no internal mutex — callers
// already guarantee single-writer per session and never interleave a stale
// slice over a newer one.
type trafficStore struct {
	dir string
	log *slog.Logger
}

// Traffic returns a per-session traffic archive store rooted at
// <dataDir>/traffic/ (the data directory is created lazily on first write).
func (fs *FileStore) Traffic() store.TrafficStore {
	return &trafficStore{
		dir: filepath.Join(fs.cfg.DataDir, "traffic"),
		log: fs.log,
	}
}

// SaveSessionTraffic atomically persists a session's full traffic slice.
// An empty slice removes the archive file (idempotent).
func (s *trafficStore) SaveSessionTraffic(ctx context.Context, sessionID string, entries []*capture.TrafficEntry) error {
	return writeSessionJSON(s.dir, sessionID, entries)
}

// DeleteSessionTraffic removes a session's archive file (idempotent).
func (s *trafficStore) DeleteSessionTraffic(ctx context.Context, sessionID string) error {
	return deleteSessionJSON(s.dir, sessionID)
}

// LoadAllSessionTraffic returns every persisted per-session archive (startup
// restore). Corrupt or unreadable files are skipped with a warning — a single
// bad file must not fail the whole restore; the session itself decides whether
// the archive is still within its retention window.
func (s *trafficStore) LoadAllSessionTraffic(ctx context.Context) (map[string][]*capture.TrafficEntry, error) {
	return loadAllSessionJSON[capture.TrafficEntry](s.dir, s.log, "traffic")
}
