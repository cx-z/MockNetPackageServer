package file

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// writeSessionJSON atomically persists a session's full entry slice as one
// JSON file under dir (<dir>/<sessionID>.json), mirroring the tmp + fsync +
// rename pattern used by every per-session archive store. An empty slice
// removes the archive file (idempotent).
func writeSessionJSON[T any](dir, sessionID string, entries []*T) error {
	if len(entries) == 0 {
		return deleteSessionJSON(dir, sessionID)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, sessionID+".json")
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
	syncDir(dir)
	return nil
}

// deleteSessionJSON removes a session's archive file under dir (idempotent).
func deleteSessionJSON(dir, sessionID string) error {
	err := os.Remove(filepath.Join(dir, sessionID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loadAllSessionJSON returns every persisted per-session archive under dir
// (startup restore). Corrupt or unreadable files are skipped with a warning —
// a single bad file must not fail the whole restore; archiveKind names the
// store in warning logs (e.g. "longconn", "traffic").
func loadAllSessionJSON[T any](dir string, log *slog.Logger, archiveKind string) (map[string][]*T, error) {
	out := make(map[string][]*T)
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // no archive directory yet — nothing to restore
		}
		return nil, err
	}
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			log.Warn(archiveKind+" archive: read session file failed, skipping", "session", sid, "error", err)
			continue
		}
		var list []*T
		if err := json.Unmarshal(data, &list); err != nil {
			log.Warn(archiveKind+" archive: parse session file failed, skipping", "session", sid, "error", err)
			continue
		}
		out[sid] = list
	}
	return out, nil
}
