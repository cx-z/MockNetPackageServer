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
	// StaticImage means a large image body (image/* over ImageInlineLimit):
	// bytes are NOT stored, the entry keeps a placeholder plus the original
	// URL for the Web preview card. Small images keep the legacy inline path.
	StaticImage StaticKind = "image"
	// StaticAudio / StaticPDF are reserved for a later milestone (add one
	// staticClassTable entry each; the Web card branch keys off the returned
	// StaticKind, not the MIME string).
)

// ImageInlineLimit is the inline ceiling for image bodies (M15 方案 D 阶段二):
// an image response or upload request body at or below this size keeps the
// legacy inline base64 path; above it the bytes are dropped and only metadata
// (+ original URL) is stored.
//
// It MUST equal the SDK capture cap (iOS MockNetPackURLProtocol.bodyLimit /
// Android MockNetPack.BODY_LIMIT_BYTES = 1 MiB). The SDK never uploads more
// than that, so a body larger than this limit could only have arrived
// truncated — keeping a "partial" inline image would be worse than dropping it.
// The three repos are checked out independently, so the equality is pinned on
// each side against the same literal (contract text lives in
// openapi/mocknetpack.yaml); this side by TestImageInlineLimit_MatchesContract.
const ImageInlineLimit int64 = 1 << 20

// staticClassTable is the Content-Type whitelist for the always-static kinds
// (video, size-independent). image 不走这张表：M15 起由
// ClassifyResponseWithSize 按 ImageInlineLimit 判 StaticImage（小图仍内联）。
// 未来加 audio / pdf 可直接加条目，ingestion 与前端卡片分支无需改动。
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
	ct := normalizeContentType(contentType)
	return staticClassTable[ct]
}

// ClassifyResponseWithSize is ClassifyResponse plus the M15 image size rule:
// a video stays StaticVideo regardless of size, while an image/* body becomes
// StaticImage only when it exceeds ImageInlineLimit. sizeBytes <= 0 means "size
// unknown" and keeps the image on the legacy inline path — dropping bytes that
// might be complete is worse than keeping a large one.
func ClassifyResponseWithSize(contentType string, sizeBytes int64) StaticKind {
	if kind := ClassifyResponse(contentType); kind != StaticNone {
		return kind
	}
	if sizeBytes > ImageInlineLimit && isImageContentType(contentType) {
		return StaticImage
	}
	return StaticNone
}

// isImageContentType reports whether the normalized type is image/*.
func isImageContentType(contentType string) bool {
	return strings.HasPrefix(normalizeContentType(contentType), "image/")
}

// normalizeContentType lowercases the media type and drops parameters
// (e.g. "image/jpeg; charset=binary" → "image/jpeg").
func normalizeContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// RequestContentType extracts the first Content-Type header value from a
// request's multi-value header map (case-insensitive). Same shape as
// ResponseContentType; kept separate so callers reading a request are not
// misled by the name.
func RequestContentType(headers map[string][]string) string {
	return ResponseContentType(headers)
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
	ct := normalizeContentType(contentType)
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
