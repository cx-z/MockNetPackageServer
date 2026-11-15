package capture

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/idna"
)

const H5MockScope = "h5"

// H5MockableAdapter reports which page APIs can replay a canned HTTP response.
func H5MockableAdapter(adapter string) bool {
	return adapter == "wkwebview-fetch" || adapter == "wkwebview-xhr"
}

// HTTPOrigin extracts the browser-compatible scheme, host and non-default port.
func HTTPOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false
		}
		host = ip.String()
		if ip.Is4In6() {
			bytes := ip.As4()
			host = fmt.Sprintf("::ffff:%x:%x", uint16(bytes[0])<<8|uint16(bytes[1]), uint16(bytes[2])<<8|uint16(bytes[3]))
		}
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" || strings.ContainsAny(host, "%:/?#@[]\\") {
			return "", false
		}
		host = strings.ToLower(host)
		// Browsers reinterpret numeric host suffixes as IPv4. Require an
		// unambiguous, canonical dotted-quad for those inputs.
		last := strings.Split(strings.TrimSuffix(host, "."), ".")
		if numericHostLabel(last[len(last)-1]) {
			ip, err := netip.ParseAddr(strings.TrimSuffix(host, "."))
			if err != nil || !ip.Is4() {
				return "", false
			}
			host = ip.String()
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", false
		}
		if (u.Scheme != "http" || n != 80) && (u.Scheme != "https" || n != 443) {
			return u.Scheme + "://" + net.JoinHostPort(host, strconv.FormatUint(n, 10)), true
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, true
}

func numericHostLabel(label string) bool {
	if strings.HasPrefix(label, "0x") {
		return strings.Trim(label[2:], "0123456789abcdef") == ""
	}
	return label != "" && strings.Trim(label, "0123456789") == ""
}

// ValidH5MockKey accepts a browser-encoded pathname as copied from URL.pathname.
// Percent escapes are retained verbatim because their casing is part of the key.
func ValidH5MockKey(method, path string) bool {
	switch strings.ToUpper(method) {
	case "CONNECT", "TRACE", "TRACK":
		return false
	}
	if len(method) > 32 || !httpguts.ValidHeaderFieldName(method) ||
		!strings.HasPrefix(path, "/") || len(path) > 4096 {
		return false
	}
	for _, c := range path {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune("\\?#<>\"\x60{}", c) {
			return false
		}
	}
	if _, err := url.PathUnescape(path); err != nil {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		dots := strings.ReplaceAll(strings.ToLower(segment), "%2e", ".")
		if dots == "." || dots == ".." {
			return false
		}
	}
	return true
}

// ValidH5MockResponse checks the response shapes the page adapters can replay.
func ValidH5MockResponse(method string, response MockResponse) bool {
	status := response.StatusCode
	if (status < 200 || status > 299) && (status < 400 || status > 599) {
		return false
	}
	if response.BodyBase64 != "" || response.Media != nil || len(response.Body) > 1024*1024 ||
		strings.HasPrefix(response.Body, "[正文未保留") || strings.Contains(response.Body, "[正文已截断]") ||
		strings.HasPrefix(response.Body, "[binary ") ||
		(strings.HasPrefix(response.Body, "[") && strings.HasSuffix(response.Body, " → 原 URL]")) {
		return false
	}
	for name, value := range response.Headers {
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return false
		}
		for _, c := range value {
			// Headers accepts WebIDL ByteString values.
			if c > 255 {
				return false
			}
		}
		if strings.EqualFold(name, "Content-Type") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "text/event-stream") {
			return false
		}
	}
	return response.Body == "" || (!strings.EqualFold(method, http.MethodHead) && status != 204 && status != 205)
}
