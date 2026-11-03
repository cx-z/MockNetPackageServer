package capture

import (
	"strconv"
	"strings"
)

// StaticKind classifies a captured response as a whitelisted static resource
// ( 方案 D：视频响应预览与大Body存储治理). Static resources are
// immutable CDN files whose URL is a permanent pointer — the server drops
// their bytes at ingestion and the browser replays the original URL instead.
type StaticKind string

const (
	// StaticNone means a business API response: bytes go through the normal
	// UTF-8 / base64 path and are stored with the entry.
	StaticNone StaticKind = ""
	// StaticVideo means a video resource: bytes are NOT stored; the entry keeps
	// only metadata plus the original URL (entry.URL) for browser replay.
	StaticVideo StaticKind = "video"
	// StaticAudio / StaticPDF are reserved for a later milestone (add one
	// staticClassTable entry each; the Web card branch keys off the returned
	// StaticKind, not the MIME string).
)

// staticClassTable is the Content-Type whitelist. 首版只配 video；未来加
// audio / pdf / image 直接加条目，ingestion 与前端卡片分支无需改动。
var staticClassTable = map[string]StaticKind{
	"video/mp4":        StaticVideo,
	"video/quicktime":  StaticVideo,
	"video/webm":       StaticVideo,
	"video/ogg":        StaticVideo,
	"video/x-matroska": StaticVideo,
}

// ClassifyResponse reports whether a response Content-Type is a whitelisted
// static resource. Parameters (e.g. "; charset=utf-8") and casing are ignored;
// an empty or unlisted type returns StaticNone (原有 base64/UTF-8 路径).
func ClassifyResponse(contentType string) StaticKind {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return staticClassTable[ct]
}

// ResponseContentType extracts the first Content-Type header value from a
// multi-value header map (case-insensitive). Returns "" when absent.
func ResponseContentType(headers map[string][]string) string {
	for k, vs := range headers {
		if strings.EqualFold(k, "Content-Type") && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// StaticPlaceholder builds the display placeholder written into
// TrafficEntry.ResponseBody for a whitelisted static resource, e.g.
// "[video/mp4 2.1 MB → 原 URL]". sizeBytes is the captured byte size; 0 omits
// the size. contentType is the original header value (parameters stripped).
func StaticPlaceholder(kind StaticKind, contentType string, sizeBytes int64) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ct == "" {
		ct = string(kind)
	}
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(ct)
	if sizeBytes > 0 {
		b.WriteByte(' ')
		b.WriteString(HumanBytes(sizeBytes))
	}
	b.WriteString(" → 原 URL]")
	return b.String()
}

// HumanBytes renders a byte count in a compact human-readable form (e.g.
// "2.1 MB", "1.4 KB", "512 B"). Negative or zero yields "0 B".
func HumanBytes(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MB"
	}
}
