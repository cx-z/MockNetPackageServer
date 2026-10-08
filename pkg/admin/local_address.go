// MockNetPack local-address API : the Web UI is
// usually opened as http://localhost:<port>/mocknetpack/ during development,
// so location.origin encodes "localhost" into the scan QR (u=...) — a real
// phone cannot reach its own localhost. This endpoint tells the browser the
// server's LAN-reachable origin (non-loopback IPv4 + the request's port) so
// the QR encodes an address the phone can actually connect to.
//
// Scope: development/Debug convenience only; non-localhost deployments
// (domain or LAN-IP access) never call it (the Web falls back to
// location.origin).

package admin

import (
	"net"
	"net/http"
	"strings"
)

// LocalAddressResponse is the GET /local-address reply.
type LocalAddressResponse struct {
	Origin string `json:"origin"` // e.g. "http://192.168.1.3:4290" (scheme + host + port, no path)
}

// lanIPv4 returns the first up, non-loopback IPv4 address of the host
// (preferring RFC1918 private ranges — the actual reachable LAN address for a
// phone on the same Wi-Fi). Empty string when the host has no LAN interface.
func lanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			if isPrivateIPv4(ip) {
				return ip.String()
			}
		}
	}
	// Fallback: any non-loopback IPv4 (e.g. docker0, tailscale…) rather than none.
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.To4() == nil {
				continue
			}
			if !ipnet.IP.To4().IsLoopback() {
				return ipnet.IP.To4().String()
			}
		}
	}
	return ""
}

// isPrivateIPv4 reports whether ip is in RFC1918 (10/8, 172.16/12, 192.168/16)
// or link-local (169.254/16) space — the addresses a same-LAN phone can use.
func isPrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch {
	case ip[0] == 10:
		return true
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		return true
	case ip[0] == 192 && ip[1] == 168:
		return true
	case ip[0] == 169 && ip[1] == 254:
		return true
	default:
		return false
	}
}

// requestOrigin builds "scheme://host[:port]" from the request Host header,
// substituting the LAN IPv4 for the host part. Port is preserved from Host so
// the QR always points at the port the browser actually used.
func requestOrigin(r *http.Request, ip string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	port := ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") { // IPv6 literal
		host = strings.Trim(host, "[]")
	}
	if ip == "" {
		ip = host // keep original host when no LAN address is available
	}
	if port != "" {
		return scheme + "://" + ip + ":" + port
	}
	return scheme + "://" + ip
}

// handleLocalAddress handles GET /api/v1/local-address (requireAuth): the
// server's LAN-reachable origin, used by the Web to build a phone-reachable
// scan QR when the browser is on localhost.
func (a *API) handleLocalAddress(w http.ResponseWriter, r *http.Request) {
	ip := lanIPv4()
	if ip == "" {
		writeError(w, http.StatusServiceUnavailable, "lan_unavailable", "server has no LAN interface; open the Web via a LAN IP and retry")
		return
	}
	writeJSON(w, http.StatusOK, LocalAddressResponse{Origin: requestOrigin(r, ip)})
}
