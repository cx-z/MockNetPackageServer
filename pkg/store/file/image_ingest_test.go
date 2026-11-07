package file

import (
	"context"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// M15 方案 D 阶段二：大图字节治理的入库分流测试。
// 规则：image/* 且字节 > ImageInlineLimit（1 MiB）→ 丢弃字节、只留占位 + 原 URL；
// 请求体一侧额外要求本次上传成功（2xx），失败上传保留截断样本供排查。

// imageEntry builds a captured image response as the SDK reports it: the
// placeholder carries the full size (the SDK caps the sample at 1 MiB).
func imageEntry(url, contentType string, size int64) *capture.TrafficEntry {
	return &capture.TrafficEntry{
		Method:      "GET",
		URL:         url,
		Timestamp:   time.Now(),
		DurationMs:  9,
		StatusCode:  200,
		ResponseHeaders: map[string][]string{
			"Content-Type":   {contentType},
			"Content-Length": {itoa(size)},
		},
		ResponseBody:       "[binary " + itoa(size) + " bytes]",
		ResponseBodyBase64: "iVBORw0KGgoAAAA",
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestCaptureManager_UploadTraffic_DropsBigImageBytes(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()
	s := newAppSession(t, m)

	const bigSize = int64(7441522) // 7.1 MB，> 1 MiB
	entries := []*capture.TrafficEntry{
		imageEntry("https://cdn.example.com/big-banner.jpg", "image/jpeg", bigSize),
		imageEntry("https://cdn.example.com/small-icon.png", "image/png", 512*1024),
	}
	now := time.Now()
	entries[0].Timestamp = now
	entries[1].Timestamp = now.Add(time.Millisecond)

	count, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries)
	if err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}

	big := list[0]
	if big.ResponseStaticKind != capture.StaticImage {
		t.Errorf("big image ResponseStaticKind = %q, want %q", big.ResponseStaticKind, capture.StaticImage)
	}
	if big.ResponseBodyBase64 != "" {
		t.Errorf("big image base64 kept (%d chars), want dropped", len(big.ResponseBodyBase64))
	}
	if want := "[image/jpeg 7.1 MB → 原 URL]"; big.ResponseBody != want {
		t.Errorf("big image placeholder = %q, want %q", big.ResponseBody, want)
	}
	if big.RequestStaticKind != capture.StaticNone {
		t.Errorf("response-only entry RequestStaticKind = %q, want empty", big.RequestStaticKind)
	}

	// ≤ 1 MiB 的图仍走内联 base64（原有路径不受影响）。
	small := list[1]
	if small.ResponseStaticKind != capture.StaticNone {
		t.Errorf("small image ResponseStaticKind = %q, want empty (inline path)", small.ResponseStaticKind)
	}
	if small.ResponseBodyBase64 == "" {
		t.Error("small image base64 dropped, want kept")
	}
}

func TestCaptureManager_UploadTraffic_DropsBigImageUploadBody(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()
	s := newAppSession(t, m)

	const bigSize = int64(5242880) // 5 MB
	build := func(status int) *capture.TrafficEntry {
		return &capture.TrafficEntry{
			Method:    "POST",
			URL:       "https://api.example.com/v1/avatar/upload",
			Timestamp: time.Now(),
			DurationMs: 120,
			StatusCode: status,
			RequestHeaders: map[string][]string{
				"Content-Type":   {"image/png"},
				"Content-Length": {itoa(bigSize)},
			},
			RequestBody:       "[binary " + itoa(bigSize) + " bytes]",
			RequestBodyBase64: "iVBORw0KGgoAAAA",
			ResponseHeaders:   map[string][]string{"Content-Type": {"application/json"}},
			ResponseBody:      `{"ok":true}`,
		}
	}

	entries := []*capture.TrafficEntry{build(200), build(502)}
	entries[0].Timestamp = time.Now()
	entries[1].Timestamp = time.Now().Add(time.Millisecond)
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}

	ok := list[0]
	if ok.RequestStaticKind != capture.StaticImage {
		t.Errorf("successful upload RequestStaticKind = %q, want %q", ok.RequestStaticKind, capture.StaticImage)
	}
	if ok.RequestBodyBase64 != "" {
		t.Error("successful upload base64 kept, want dropped")
	}
	if want := "[image/png 5.0 MB → 原 URL]"; ok.RequestBody != want {
		t.Errorf("upload placeholder = %q, want %q", ok.RequestBody, want)
	}
	if ok.ResponseStaticKind != capture.StaticNone {
		t.Errorf("json response classified as %q, want empty", ok.ResponseStaticKind)
	}

	// 上传失败：保留截断样本，Web 仍能看到当时实际发出的前 1 MiB。
	failed := list[1]
	if failed.RequestStaticKind != capture.StaticNone {
		t.Errorf("failed upload RequestStaticKind = %q, want empty (keep sample)", failed.RequestStaticKind)
	}
	if failed.RequestBodyBase64 == "" {
		t.Error("failed upload base64 dropped, want kept for troubleshooting")
	}
}
