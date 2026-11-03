package file

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// 方案 D：视频响应预览与大Body存储治理 —— 静态资源（video 白名单）不存字节
// 的 ingestion 分流、规则创建 302 回放分支与编辑置位测试。

// newAppSession registers a device and activates a session, mirroring the
// helper pattern used across capture_registry_test.go.
func newAppSession(t *testing.T, m *store.CaptureManager) *capture.CaptureSession {
	t.Helper()
	if _, err := m.RegisterDevice(context.Background(), &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("RegisterDevice() = %v", err)
	}
	s, _, err := m.ActivateSession(context.Background(), "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	return s
}

// videoEntry builds a captured video response exactly as the SDK would report
// it: binary body with the "[binary N bytes]" placeholder + base64 bytes.
func videoEntry(url string, ts time.Time) *capture.TrafficEntry {
	return &capture.TrafficEntry{
		Method:      "GET",
		URL:         url,
		Timestamp:   ts,
		DurationMs:  12,
		StatusCode:  200,
		ResponseHeaders: map[string][]string{
			"Content-Type":   {"video/mp4"},
			"Content-Length": {"2165957"},
		},
		ResponseBody:       "[binary 2165957 bytes]",
		ResponseBodyBase64: "AAAAIGZ0eXBpc29tAAAC",
	}
}

func TestCaptureManager_UploadTraffic_DropsVideoBytes(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()
	s := newAppSession(t, m)

	now := time.Now()
	entries := []*capture.TrafficEntry{
		videoEntry("https://video-test.aimoox.com/pixelmusevd/a.mp4", now),
		{ // 非白名单业务 API：原有 UTF-8/base64 路径不受影响。
			Method:      "GET",
			URL:         "https://api.example.com/feed",
			Timestamp:   now.Add(time.Millisecond),
			DurationMs:  3,
			StatusCode:  200,
			ResponseHeaders: map[string][]string{"Content-Type": {"application/json"}},
			ResponseBody:     `{"ok":true}`,
			ResponseBodyBase64: "eyJvayI6dHJ1ZX0=",
		},
	}
	count, err := m.UploadTraffic(ctx, "app", "d1", s.ID, entries)
	if err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	list, total, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(list))
	}

	video := list[0]
	if video.ResponseStaticKind != capture.StaticVideo {
		t.Errorf("ResponseStaticKind = %q, want %q", video.ResponseStaticKind, capture.StaticVideo)
	}
	if video.ResponseBodyBase64 != "" {
		t.Errorf("video base64 kept: %q, want dropped (empty)", video.ResponseBodyBase64)
	}
	if want := "[video/mp4 2.1 MB → 原 URL]"; video.ResponseBody != want {
		t.Errorf("video placeholder = %q, want %q", video.ResponseBody, want)
	}

	api := list[1]
	if api.ResponseStaticKind != capture.StaticNone {
		t.Errorf("api ResponseStaticKind = %q, want empty", api.ResponseStaticKind)
	}
	if api.ResponseBody != `{"ok":true}` || api.ResponseBodyBase64 == "" {
		t.Errorf("api body mutated: body=%q base64=%q", api.ResponseBody, api.ResponseBodyBase64)
	}
}

// TestMockRule_CreateFromVideoTraffic_Redirects verifies the approved replay
// branch: a rule created from an unedited video entry carries a 302 redirect
// to the original URL instead of stored bytes; non-video entries keep their
// captured response and still record OriginTrafficID for provenance.
func TestMockRule_CreateFromVideoTraffic_Redirects(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()
	s := newAppSession(t, m)

	now := time.Now()
	url := "https://video-test.aimoox.com/pixelmusevd/a.mp4"
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{videoEntry(url, now)}); err != nil {
		t.Fatalf("UploadTraffic(video) = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	videoID := list[0].ID

	in := &capture.MockRuleInput{
		Method: "GET",
		Path:   "/pixelmusevd/a.mp4",
		// Web/MCP 在创建时传回的是抓到的真实回包（206 + 二进制占位），服务端
		// 对未编辑视频规则整体替换为 302，故此处刻意带上"本应保存"的回包。
		Response: capture.MockResponse{
			StatusCode: 206,
			Headers:    map[string]string{"Content-Type": "video/mp4"},
			Body:       "[binary 2165957 bytes]",
		},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/pixelmusevd/a.mp4"},
		OriginTrafficID: videoID,
	}
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
	if err != nil {
		t.Fatalf("CreateMockRule(video) = %v", err)
	}
	if view.Response.StatusCode != 302 {
		t.Errorf("video rule status = %d, want 302 (redirect, no stored bytes)", view.Response.StatusCode)
	}
	if got := view.Response.Headers["Location"]; got != url {
		t.Errorf("Location = %q, want %q", got, url)
	}
	if view.Response.Body != "" || view.Response.BodyBase64 != "" {
		t.Errorf("video rule body kept: body=%q base64=%q, want empty", view.Response.Body, view.Response.BodyBase64)
	}
	if view.OriginTrafficID != videoID {
		t.Errorf("OriginTrafficID = %q, want %q", view.OriginTrafficID, videoID)
	}
	if view.Edited {
		t.Error("Edited = true on fresh video rule, want false")
	}

	// 非视频流量：OriginTrafficID 照常记录，但回包不被改写。
	apiURL := "https://api.example.com/feed"
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{{
		Method: "GET", URL: apiURL, Timestamp: now.Add(2 * time.Millisecond), DurationMs: 3,
		StatusCode: 200, ResponseHeaders: map[string][]string{"Content-Type": {"application/json"}},
		ResponseBody: `{"ok":true}`, ResponseBodyBase64: "eyJvayI6dHJ1ZX0=",
	}}); err != nil {
		t.Fatalf("UploadTraffic(api) = %v", err)
	}
	list, _, _ = m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	apiID := list[1].ID
	in2 := &capture.MockRuleInput{
		Method: "GET", Path: "/feed",
		Response:        capture.MockResponse{StatusCode: 200, Body: `{"ok":true}`},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/feed"},
		OriginTrafficID: apiID,
	}
	view2, _, err := m.CreateMockRule(ctx, "app", "d1", in2, nil)
	if err != nil {
		t.Fatalf("CreateMockRule(api) = %v", err)
	}
	if view2.Response.StatusCode != 200 || view2.Response.Body != `{"ok":true}` {
		t.Errorf("api rule response rewritten: %+v, want 200 {\"ok\":true}", view2.Response)
	}
	if view2.OriginTrafficID != apiID {
		t.Errorf("api rule OriginTrafficID = %q, want %q", view2.OriginTrafficID, apiID)
	}

	// 来源条目已不存在（会话清理等）→ 创建失败并报 not found，不静默降级。
	bad := &capture.MockRuleInput{
		Method: "GET", Path: "/gone",
		Response:        capture.MockResponse{StatusCode: 200},
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/gone"},
		OriginTrafficID: "no-such-traffic-id",
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", bad, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreateMockRule(unknown origin) = %v, want ErrNotFound", err)
	}
}

// TestMockRule_UpdateSetsEdited verifies that ANY saved edit (here: status 500
// with an empty body) flips Edited=true so the rule leaves the 302 fast path
// and replays whatever the user configured.
func TestMockRule_UpdateSetsEdited(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()
	s := newAppSession(t, m)

	now := time.Now()
	url := "https://video-test.aimoox.com/pixelmusevd/a.mp4"
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{videoEntry(url, now)}); err != nil {
		t.Fatalf("UploadTraffic(video) = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil {
		t.Fatalf("ListSessionTraffic() = %v", err)
	}
	videoID := list[0].ID

	created, _, err := m.CreateMockRule(ctx, "app", "d1", &capture.MockRuleInput{
		Method: "GET", Path: "/pixelmusevd/a.mp4",
		Response:        capture.MockResponse{StatusCode: 206},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/pixelmusevd/a.mp4"},
		OriginTrafficID: videoID,
	}, nil)
	if err != nil {
		t.Fatalf("CreateMockRule() = %v", err)
	}
	if created.Response.StatusCode != 302 {
		t.Fatalf("precondition failed: fresh video rule status = %d, want 302", created.Response.StatusCode)
	}

	note := "改为 500 空 body 测边界"
	enabled := true
	updated, _, err := m.UpdateMockRule(ctx, "app", "d1", created.ID, &capture.UpdateMockRuleInput{
		Response: capture.MockResponse{StatusCode: 500},
		Note:     note,
		Enabled:  &enabled,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateMockRule() = %v", err)
	}
	if !updated.Edited {
		t.Error("Edited = false after edit save, want true")
	}
	if updated.Response.StatusCode != 500 {
		t.Errorf("status = %d, want 500 (user-configured body now controls replay)", updated.Response.StatusCode)
	}
	if updated.Response.Headers["Location"] != "" {
		t.Errorf("stale Location header kept: %q, want gone", updated.Response.Headers["Location"])
	}
	if updated.OriginTrafficID != videoID {
		t.Errorf("OriginTrafficID lost on edit: %q, want %q", updated.OriginTrafficID, videoID)
	}
	if !strings.Contains(updated.Note, "500") {
		t.Errorf("note not saved: %q", updated.Note)
	}
}
