package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// File: mediafetch.go
// 大图规则创建时的回源取数（M15 方案 D 阶段二）.
// ============================================================================
// Media fetch
// ============================================================================
//
// When "Mock 此请求" creates a rule from a captured big-image response, the
// server must obtain the bytes itself: the SDK never uploaded them (they were
// dropped at ingestion). The original request headers are forwarded on a
// whitelist so CDN/auth-protected images can still be fetched, and every
// failure maps to one of the stable reason codes below — the Web/MCP caller
// shows that code, the rule is NOT created.

// MediaFetchError is the failure of a media fetch, carrying a stable reason
// code (`url_empty` / `missing_auth` / `http_4xx` / `http_5xx` / `timeout` /
// `too_large` / `redirect_limit` / `network`, plus `storage_full` for a media
// directory at its cap).
type MediaFetchError struct {
	Code    string
	Message string
}

func (e *MediaFetchError) Error() string {
	if e.Message != "" {
		return "media fetch failed (" + e.Code + "): " + e.Message
	}
	return "media fetch failed (" + e.Code + ")"
}

// stable media fetch reason codes (contract 400 media_unfetchable.code).
const (
	MediaReasonURLEmpty      = "url_empty"
	MediaReasonMissingAuth   = "missing_auth"
	MediaReasonHTTP4xx       = "http_4xx"
	MediaReasonHTTP5xx       = "http_5xx"
	MediaReasonTimeout       = "timeout"
	MediaReasonTooLarge      = "too_large"
	MediaReasonRedirectLimit = "redirect_limit"
	MediaReasonNetwork       = "network"
	MediaReasonStorageFull   = "storage_full"
)

const (
	// mediaFetchTimeout bounds one fetch (connect + redirects + body).
	mediaFetchTimeout = 15 * time.Second
	// mediaFetchMaxRedirects is the redirect budget before redirect_limit.
	mediaFetchMaxRedirects = 3
)

// mediaFetchHeaders is the request-header whitelist forwarded to the origin.
// Only credentials/negotiation/referer survive: hop-by-hop and body headers
// (Content-Length, Transfer-Encoding, Range …) must not be replayed.
var mediaFetchHeaders = []string{
	"Authorization", "Cookie", "User-Agent", "Referer", "Accept", "Accept-Language",
}

// FetchMedia downloads a captured static resource from its original URL.
// A 2xx body is returned with its Content-Type; everything else is a
// *MediaFetchError. The size is bounded while reading (MaxMediaBytes + 1) so a
// huge origin response never fully materialises in memory.
func FetchMedia(ctx context.Context, rawURL string, originHeaders map[string][]string) ([]byte, string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, "", &MediaFetchError{Code: MediaReasonURLEmpty, Message: "source traffic has no original URL"}
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", &MediaFetchError{Code: MediaReasonURLEmpty, Message: "original URL is not an absolute http(s) URL"}
	}

	ctx, cancel := context.WithTimeout(ctx, mediaFetchTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: mediaFetchTimeout,
		// Redirects are followed manually so the budget can be counted and the
		// final URL reported; the transport keeps the default TLS behavior.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// via 是「已发出的请求」：跟第 3 跳时 len(via)==3 仍应放行，第 4 跳
			// 才超预算。写成 >= 会把预算偷偷变成 2 跳，与 redirect_limit 的契约不符。
			if len(via) > mediaFetchMaxRedirects {
				return errMediaRedirectLimit
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", &MediaFetchError{Code: MediaReasonNetwork, Message: err.Error()}
	}
	hasCredential := false
	for _, h := range mediaFetchHeaders {
		if v := firstHeader(originHeaders, h); v != "" {
			req.Header.Set(h, v)
			if h == "Authorization" || h == "Cookie" {
				hasCredential = true
			}
		}
	}
	// 无凭证的请求也不强塞 Accept：原请求的 Accept 若为 JSON，CDN 仍会按扩展名
	// 返回图片；缺失时交给默认值（*/*），避免「Accept: application/json」把图拒掉。

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", classifyMediaFetchError(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := MediaReasonHTTP4xx
		if resp.StatusCode >= 500 {
			code = MediaReasonHTTP5xx
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !hasCredential {
			code = MediaReasonMissingAuth
		}
		return nil, "", &MediaFetchError{
			Code:    code,
			Message: fmt.Sprintf("origin returned %d", resp.StatusCode),
		}
	}

	limited := io.LimitReader(resp.Body, MaxMediaBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", classifyMediaFetchError(ctx, err)
	}
	if int64(len(data)) > MaxMediaBytes {
		return nil, "", &MediaFetchError{
			Code:    MediaReasonTooLarge,
			Message: fmt.Sprintf("origin body exceeds %d bytes", MaxMediaBytes),
		}
	}
	if len(data) == 0 {
		return nil, "", &MediaFetchError{Code: MediaReasonNetwork, Message: "origin returned an empty body"}
	}

	contentType := normalizeMediaContentType(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return data, contentType, nil
}

// errMediaRedirectLimit is the sentinel returned by CheckRedirect.
var errMediaRedirectLimit = errors.New("media fetch redirect limit reached")

// classifyMediaFetchError maps transport errors to stable reason codes.
func classifyMediaFetchError(ctx context.Context, err error) error {
	if errors.Is(err, errMediaRedirectLimit) {
		return &MediaFetchError{Code: MediaReasonRedirectLimit, Message: err.Error()}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return &MediaFetchError{Code: MediaReasonTimeout, Message: "origin did not respond in time"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &MediaFetchError{Code: MediaReasonTimeout, Message: "origin did not respond in time"}
	}
	return &MediaFetchError{Code: MediaReasonNetwork, Message: err.Error()}
}

// firstHeader returns the first value of a case-insensitive header lookup.
func firstHeader(headers map[string][]string, name string) string {
	for k, vs := range headers {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// normalizeMediaContentType lowercases the media type and keeps the parameters
// out of the SDK-facing value (SDK sets it as the replay Content-Type as-is).
func normalizeMediaContentType(ct string) string {
	return strings.TrimSpace(strings.Split(ct, ";")[0])
}

// MediaSHA256 is the hex content hash used in MediaRef (and by the SDK LRU key).
func MediaSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// verifyMediaHash reports whether data still matches the stored ref.
func verifyMediaHash(data []byte, ref *capture.MediaRef) bool {
	return ref != nil && ref.SHA256 != "" && MediaSHA256(data) == ref.SHA256
}

// MockMediaMinSDKVersion is the first SDK version that understands
// MockResponse.media (fetches the blob instead of replaying Body). Rules
// created from a big-image capture only become media rules for devices at or
// above it; older SDKs keep the legacy byte-less rule.
//
// 首发版本：iOS 1.1.1（1.1.0 的补丁版，随 M15 一起发）、Android 1.2.0。
// 门限取两者较早者；Android 没有 1.1.x 版本，不会误放行。
const MockMediaMinSDKVersion = "1.1.1"

// supportsMockMedia reports whether an SDK version can replay a media rule.
// Empty/unparsable versions are treated as unsupported (fail safe: the device
// gets the legacy rule instead of one it cannot render).
func supportsMockMedia(sdkVersion string) bool {
	got, ok := parseSemver(sdkVersion)
	if !ok {
		return false
	}
	min, _ := parseSemver(MockMediaMinSDKVersion)
	for i := 0; i < 3; i++ {
		if got[i] != min[i] {
			return got[i] > min[i]
		}
	}
	return true
}

// parseSemver parses "1.2.3", "v1.2.3", "1.2.3-beta.1", "1.2". Requires at
// least major.minor; missing components default to 0.
func parseSemver(v string) ([3]int, bool) {
	out := [3]int{}
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if s == "" {
		return out, false
	}
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
