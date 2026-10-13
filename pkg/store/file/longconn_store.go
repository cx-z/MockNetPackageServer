package file

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	if len(entries) == 0 {
		return s.DeleteSessionLongConn(ctx, sessionID)
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, sessionID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(s.dir)
	return nil
}

// DeleteSessionLongConn removes a session's archive file (idempotent).
func (s *longConnStore) DeleteSessionLongConn(ctx context.Context, sessionID string) error {
	err := os.Remove(filepath.Join(s.dir, sessionID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadAllSessionLongConn returns every persisted per-session longconn archive
// (startup restore). Corrupt/unreadable files are skipped with a warning.
func (s *longConnStore) LoadAllSessionLongConn(ctx context.Context) (map[string][]*capture.LongConnectionRecord, error) {
	out := make(map[string][]*capture.LongConnectionRecord)
	des, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // no longconn directory yet — nothing to restore
		}
		return nil, err
	}
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			s.log.Warn("longconn archive: read session file failed, skipping", "session", sid, "error", err)
			continue
		}
		var list []*capture.LongConnectionRecord
		if err := json.Unmarshal(data, &list); err != nil {
			s.log.Warn("longconn archive: parse session file failed, skipping", "session", sid, "error", err)
			continue
		}
		out[sid] = list
	}
	return out, nil
}
