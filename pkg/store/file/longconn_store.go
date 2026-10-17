package file

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// longConnStore implements store.LongConnStore as one JSON file per capture
// session under <dataDir>/longconn/<sessionID>.json (能力线 L1: 长连接日志
// 与 HTTP 流量分开存储、独立额度). Writes are atomic (tmp + fsync + rename),
// mirroring trafficStore; the CaptureManager serializes all mutations of one
// session's slice under longconnMu, so no internal mutex is needed.
type longConnStore struct {
	dir string
	log *slog.Logger
}

// LongConn returns a per-session longconn archive store rooted at
// <dataDir>/longconn/ (the data directory is created lazily on first write).
func (fs *FileStore) LongConn() store.LongConnStore {
	return &longConnStore{
		dir: filepath.Join(fs.cfg.DataDir, "longconn"),
		log: fs.log,
	}
}

// SaveSessionLongConn atomically persists a session's full longconn slice.
// An empty slice removes the archive file (idempotent).
func (s *longConnStore) SaveSessionLongConn(ctx context.Context, sessionID string, entries []*capture.LongConnectionRecord) error {
	return writeSessionJSON(s.dir, sessionID, entries)
}

// DeleteSessionLongConn removes a session's archive file (idempotent).
func (s *longConnStore) DeleteSessionLongConn(ctx context.Context, sessionID string) error {
	return deleteSessionJSON(s.dir, sessionID)
}

// LoadAllSessionLongConn returns every persisted per-session longconn archive
// (startup restore). Corrupt/unreadable files are skipped with a warning.
func (s *longConnStore) LoadAllSessionLongConn(ctx context.Context) (map[string][]*capture.LongConnectionRecord, error) {
	return loadAllSessionJSON[capture.LongConnectionRecord](s.dir, s.log, "longconn")
}
