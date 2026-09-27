package admin

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login failure-based lockout policy (4.16): a username or source IP that
// accumulates LoginFailureLimit failed logins inside LoginFailureWindow is
// locked out until the window slides past the earliest failure. This stops
// both credential stuffing against one account and distributed username
// enumeration from one source.
const (
	// LoginFailureLimit is the number of failed logins that triggers lockout.
	LoginFailureLimit = 5
	// LoginFailureWindow is the sliding window for counting failures.
	LoginFailureWindow = 5 * time.Minute
)

// loginThrottle implements failure-based rate limiting for the login
// endpoint. Failed attempts are tracked per-username and per-IP over a
// sliding window; a key at/over the limit is denied until its failures age
// out. Loopback IPs are deliberately not throttled (single-operator dev
// machine — matches the localhost-bypass philosophy of apiKeyConfig /
// WithAllowLocalhostBypass). In production behind a reverse proxy,
// r.RemoteAddr is the proxy, so effective per-IP protection requires the
// proxy to pass a trusted client-IP header.
type loginThrottle struct {
	mu       sync.Mutex
	window   time.Duration
	limit    int
	failures map[string][]time.Time
}

func newLoginThrottle(limit int, window time.Duration) *loginThrottle {
	return &loginThrottle{
		window:   window,
		limit:    limit,
		failures: make(map[string][]time.Time),
	}
}

func (t *loginThrottle) userKey(username string) string {
	return "u:" + strings.ToLower(strings.TrimSpace(username))
}

func (t *loginThrottle) ipKey(ip string) string { return "ip:" + ip }

// allow reports whether a login attempt for username/IP may proceed, pruning
// expired failures. It does not record the attempt itself.
func (t *loginThrottle) allow(username, ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-t.window)
	keys := []string{t.userKey(username)}
	if !isLoopbackIP(ip) {
		keys = append(keys, t.ipKey(ip))
	}
	for _, k := range keys {
		fs := t.failures[k]
		kept := fs[:0]
		for _, f := range fs {
			if f.After(cutoff) {
				kept = append(kept, f)
			}
		}
		t.failures[k] = kept
		if len(kept) >= t.limit {
			return false
		}
	}
	return true
}

// recordFailure registers a failed login attempt for username/IP.
func (t *loginThrottle) recordFailure(username, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.failures[t.userKey(username)] = append(t.failures[t.userKey(username)], now)
	if !isLoopbackIP(ip) {
		t.failures[t.ipKey(ip)] = append(t.failures[t.ipKey(ip)], now)
	}
}

// recordSuccess clears throttle state for a successful login, resetting the
// account's and source's failure history.
func (t *loginThrottle) recordSuccess(username, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, t.userKey(username))
	if !isLoopbackIP(ip) {
		delete(t.failures, t.ipKey(ip))
	}
}

// clientIP extracts the IP (without port) from a request's RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLoopbackIP reports whether ip is a loopback address (127.0.0.0/8, ::1).
func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}
