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
func (s *FileStore) Traffic() store.TrafficStore {
	return &trafficStore{
		dir: filepath.Join(s.cfg.DataDir, "traffic"),
		log: s.log,
	}
}

// SaveSessionTraffic atomically persists a session's full traffic slice.
// An empty slice removes the archive file (idempotent).
func (s *trafficStore) SaveSessionTraffic(ctx context.Context, sessionID string, entries []*capture.TrafficEntry) error {
	if len(entries) == 0 {
		return s.DeleteSessionTraffic(ctx, sessionID)
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
		_ = os.Remove(tmp) // Clean up temp file on failure
		return err
	}
	syncDir(s.dir)
	return nil
}

// DeleteSessionTraffic removes a session's archive file (idempotent).
func (s *trafficStore) DeleteSessionTraffic(ctx context.Context, sessionID string) error {
	err := os.Remove(filepath.Join(s.dir, sessionID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadAllSessionTraffic returns every persisted per-session archive (startup
// restore). Corrupt or unreadable files are skipped with a warning — a single
// bad file must not fail the whole restore; the session itself decides whether
// the archive is still within its retention window.
func (s *trafficStore) LoadAllSessionTraffic(ctx context.Context) (map[string][]*capture.TrafficEntry, error) {
	out := make(map[string][]*capture.TrafficEntry)
	des, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // no traffic directory yet — nothing to restore
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
			s.log.Warn("traffic archive: read session file failed, skipping", "session", sid, "error", err)
			continue
		}
		var list []*capture.TrafficEntry
		if err := json.Unmarshal(data, &list); err != nil {
			s.log.Warn("traffic archive: parse session file failed, skipping", "session", sid, "error", err)
			continue
		}
		out[sid] = list
	}
	return out, nil
}
