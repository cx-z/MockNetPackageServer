package capture

import "testing"

func TestClassifyResponse(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		want        StaticKind
	}{
		{"mp4", "video/mp4", StaticVideo},
		{"mp4 with charset", "video/mp4; charset=utf-8", StaticVideo},
		{"mp4 uppercase", "Video/MP4", StaticVideo},
		{"mp4 surrounded by spaces", "  video/mp4  ", StaticVideo},
		{"quicktime", "video/quicktime", StaticVideo},
		{"webm", "video/webm", StaticVideo},
		{"ogg", "video/ogg", StaticVideo},
		{"matroska", "video/x-matroska", StaticVideo},
		{"json stays legacy", "application/json", StaticNone},
		{"png stays legacy (later milestone)", "image/png", StaticNone},
		{"empty", "", StaticNone},
		{"unlisted video variant", "video/avi", StaticNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyResponse(c.contentType); got != c.want {
				t.Errorf("ClassifyResponse(%q) = %q, want %q", c.contentType, got, c.want)
			}
		})
	}
}

func TestResponseContentType(t *testing.T) {
	if got := ResponseContentType(map[string][]string{"Content-Type": {"video/mp4"}}); got != "video/mp4" {
		t.Errorf("ResponseContentType() = %q, want video/mp4", got)
	}
	// 大小写不敏感 + 取首个值。
	if got := ResponseContentType(map[string][]string{"content-type": {"video/webm", "text/html"}}); got != "video/webm" {
		t.Errorf("ResponseContentType(case+multi) = %q, want video/webm", got)
	}
	if got := ResponseContentType(map[string][]string{"X-Custom": {"1"}}); got != "" {
		t.Errorf("ResponseContentType(missing) = %q, want empty", got)
	}
	if got := ResponseContentType(nil); got != "" {
		t.Errorf("ResponseContentType(nil) = %q, want empty", got)
	}
}

func TestStaticPlaceholder(t *testing.T) {
	got := StaticPlaceholder(StaticVideo, "video/mp4; charset=utf-8", 2165957)
	want := "[video/mp4 2.1 MB → 原 URL]"
	if got != want {
		t.Errorf("StaticPlaceholder() = %q, want %q", got, want)
	}
	if got := StaticPlaceholder(StaticVideo, "video/mp4", 0); got != "[video/mp4 → 原 URL]" {
		t.Errorf("StaticPlaceholder(no size) = %q, want %q", got, "[video/mp4 → 原 URL]")
	}
	// 空 contentType 时回退 kind 字符串。
	if got := StaticPlaceholder(StaticVideo, "", 512); got != "[video 512 B → 原 URL]" {
		t.Errorf("StaticPlaceholder(empty ct) = %q, want %q", got, "[video 512 B → 原 URL]")
	}
}
