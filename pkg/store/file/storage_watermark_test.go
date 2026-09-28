package file

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getmockd/mockd/pkg/store"
)

// slogCapture is a minimal slog.Handler that records emitted records so tests
// can assert on the level/message of the O2.4 storage-watermark log line.
type slogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *slogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *slogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *slogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *slogCapture) WithGroup(string) slog.Handler      { return c }

func (c *slogCapture) drain() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs := c.records
	c.records = nil
	return rs
}

func withLogCapture(t *testing.T) *slogCapture {
	t.Helper()
	c := &slogCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(old) })
	return c
}

// O2.4 存储水位监控：启动/每小时输出一行 data.json 体积，超 500MB WARN、
// 未超 INFO、文件缺失报 size 0、空路径（未接线）不输出。
func TestCaptureManager_LogDataFileSize(t *testing.T) {
	cap := withLogCapture(t)
	m, fs := newCaptureManager(t, 0)
	_ = fs // 持久化 data.json 由 FileStore 管理；水位检查直接指到临时目录

	// 1) 未接线（空路径）：不产生任何日志
	m.LogDataFileSize()
	if got := cap.drain(); len(got) != 0 {
		t.Fatalf("empty dataFile should not log, got %d records", len(got))
	}

	// 2) 缺失文件：INFO，bytes=0 exists=false
	missing := filepath.Join(t.TempDir(), "data.json")
	m.SetDataFilePath(missing)
	m.LogDataFileSize()
	recs := cap.drain()
	if len(recs) != 1 || recs[0].Level != slog.LevelInfo {
		t.Fatalf("missing file: want 1 INFO record, got %+v", recs)
	}

	// 3) 正常小文件：INFO + human 尺寸
	dir := t.TempDir()
	small := filepath.Join(dir, "data.json")
	if err := os.WriteFile(small, []byte(`{"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.SetDataFilePath(small)
	m.LogDataFileSize()
	recs = cap.drain()
	if len(recs) != 1 || recs[0].Level != slog.LevelInfo {
		t.Fatalf("small file: want 1 INFO record, got %+v", recs)
	}
	if !strings.Contains(recs[0].Message, "data.json size") {
		t.Fatalf("unexpected message %q", recs[0].Message)
	}

	// 4) 达到/超过阈值：WARN（sparse 文件，不占真实磁盘）
	big := filepath.Join(dir, "big.json")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(store.DefaultDataFileWarnBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m.SetDataFilePath(big)
	m.LogDataFileSize()
	recs = cap.drain()
	if len(recs) != 1 || recs[0].Level != slog.LevelWarn {
		t.Fatalf("big file: want 1 WARN record, got %+v", recs)
	}
	if !strings.Contains(recs[0].Message, "exceeds warning threshold") {
		t.Fatalf("unexpected WARN message %q", recs[0].Message)
	}
}

