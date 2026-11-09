package capture

import (
	"testing"
)

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

// E1: 大图分类。视频不受大小影响；图片仅超门限（1 MiB）才转为静态分类，
// 正好等于门限、未知大小（<=0）一律保留内联路径（宁可不省字节也不误杀）。
func TestClassifyResponseWithSize(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		size        int64
		want        StaticKind
	}{
		{"big jpeg", "image/jpeg", 7441522, StaticImage},
		{"big jpeg with charset", "image/jpeg; charset=binary", 7441522, StaticImage},
		{"big png uppercase", "Image/PNG", 2097153, StaticImage},
		// 正好 1 MiB 仍内联（门限是「大于才分流」）。
		{"exactly 1 MiB stays inline", "image/png", ImageInlineLimit, StaticNone},
		{"below limit stays inline", "image/webp", 512 * 1024, StaticNone},
		{"tiny stays inline", "image/gif", 2048, StaticNone},
		// 大小未知：不分流（可能本来就是完整小图）。
		{"unknown size (0)", "image/jpeg", 0, StaticNone},
		{"unknown size (negative)", "image/jpeg", -1, StaticNone},
		// 视频一直是静态分类，与大小无关。
		{"small video", "video/mp4", 2048, StaticVideo},
		{"big video", "video/mp4", 1 << 30, StaticVideo},
		// 非图片大 body 继续走原有路径。
		{"big json", "application/json", 1 << 30, StaticNone},
		{"big octet-stream", "application/octet-stream", 1 << 30, StaticNone},
		{"empty", "", 1 << 30, StaticNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyResponseWithSize(c.contentType, c.size); got != c.want {
				t.Errorf("ClassifyResponseWithSize(%q, %d) = %q, want %q", c.contentType, c.size, got, c.want)
			}
		})
	}
}

// E1: 服务端的内联门限必须等于 SDK 抓包上限——SDK 永不上传超过 1 MiB 的 body，
// 因此“服务端见到 >1 MiB 的 image/*”只能是被截断的样本，必须分流而不是内联。
//
// 三个仓库各自独立检出、独立 CI，无法在一条测试里互读源码，所以这个数字在
// 三处各钉一次：这里、iOS MockNetPackURLProtocol.bodyLimit、Android
// MockNetPack.BODY_LIMIT_BYTES，契约文本写在 openapi/mocknetpack.yaml
// （“大于 1 MiB 的 image/*”/“SDK 抓包上限”）。任一端漂移，谁自己的 CI 先红。
//
// 注意能抓的是单边漂移：若有人把三处字面值一起改掉，本测试不会拦。
// 机器强校验跨端一致需要一条能同时 checkout 三仓库的流水线，现在没有，
// 三处同步靠评审把关（已确认接受该代价）。
func TestImageInlineLimit_MatchesContract(t *testing.T) {
	const contract int64 = 1 << 20
	if ImageInlineLimit != contract {
		t.Errorf("ImageInlineLimit = %d, want %d (keep server limit, openapi contract text and both SDK bodyLimit in sync)",
			ImageInlineLimit, contract)
	}
}
