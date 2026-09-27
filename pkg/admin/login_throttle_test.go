package admin

import (
	"net/http"
	"testing"
	"time"
)

func TestLoginThrottle_LocksOutAfterRepeatedFailures(t *testing.T) {
	th := newLoginThrottle(3, time.Minute)
	ip := "192.168.1.50"

	for i := 0; i < 3; i++ {
		if !th.allow("carol", ip) {
			t.Fatalf("attempt %d: allow = false, want true", i+1)
		}
		th.recordFailure("carol", ip)
	}
	if th.allow("carol", ip) {
		t.Fatal("4th attempt after 3 failures: allow = true, want lockout")
	}

	// Lockout is per-username AND per-IP: the same source is locked out for
	// every account (the IP key accumulated 3 failures), but a DIFFERENT
	// source stays allowed for the locked account.
	if th.allow("carol", "198.51.100.7") {
		t.Fatal("locked account from a fresh IP: allow = true, want lockout")
	}
	if !th.allow("dave", "198.51.100.7") {
		t.Fatal("different user from fresh IP: allow = false, want true")
	}

	// A successful login resets the account's failure history.
	th.recordSuccess("carol", ip)
	if !th.allow("carol", ip) {
		t.Fatal("after recordSuccess: allow = false, want reset")
	}
}

func TestLoginThrottle_LocksOutByIP(t *testing.T) {
	th := newLoginThrottle(3, time.Minute)
	// Non-loopback IP: failures from distinct usernames share the IP key.
	for i := 0; i < 3; i++ {
		u := string(rune('a' + i))
		if !th.allow(u, "203.0.113.9") {
			t.Fatalf("user %q attempt: allow = false, want true", u)
		}
		th.recordFailure(u, "203.0.113.9")
	}
	if th.allow("fresh-user", "203.0.113.9") {
		t.Fatal("fresh user from throttled IP: allow = true, want lockout")
	}
	// Success from the same IP resets the IP key.
	th.recordSuccess("a", "203.0.113.9")
	if !th.allow("fresh-user", "203.0.113.9") {
		t.Fatal("after success from same IP: allow = false, want reset")
	}
}

func TestLoginThrottle_LoopbackExempt(t *testing.T) {
	th := newLoginThrottle(2, time.Minute)
	// Loopback is deliberately not throttled: many failures from DIFFERENT
	// usernames on 127.0.0.1 never lock the shared dev IP (single-operator
	// machine, matches the localhost-bypass philosophy).
	for i := 0; i < 20; i++ {
		th.recordFailure("local-user-"+string(rune('a'+i%26))+string(rune('0'+i)), "127.0.0.1")
	}
	if !th.allow("fresh-user", "127.0.0.1") {
		t.Fatal("loopback: allow = false, want true (loopback exempt)")
	}
	// Sanity: the same volume of failures on a non-loopback IP locks the IP.
	for i := 0; i < 20; i++ {
		th.recordFailure("remote-user-"+string(rune('a'+i%26))+string(rune('0'+i)), "10.0.0.7")
	}
	if th.allow("fresh-user-2", "10.0.0.7") {
		t.Fatal("non-loopback with 20 failures: allow = true, want lockout")
	}
	// The username key still protects loopback accounts from stuffing: 20
	// failures for ONE loopback user lock that account.
	th2 := newLoginThrottle(2, time.Minute)
	for i := 0; i < 20; i++ {
		th2.recordFailure("local-user", "127.0.0.1")
	}
	if th2.allow("local-user", "127.0.0.1") {
		t.Fatal("single loopback user with 20 failures: allow = true, want lockout")
	}
}

func TestLoginThrottle_FailuresAgeOut(t *testing.T) {
	th := newLoginThrottle(2, 50*time.Millisecond)
	th.recordFailure("eve", "198.51.100.4")
	th.recordFailure("eve", "198.51.100.4")
	if th.allow("eve", "198.51.100.4") {
		t.Fatal("2 failures inside window: allow = true, want lockout")
	}
	time.Sleep(80 * time.Millisecond)
	if !th.allow("eve", "198.51.100.4") {
		t.Fatal("failures aged out of window: allow = false, want true")
	}
}

// TestAuthLoginLocksOutAfterRepeatedFailures (4.16): the login endpoint
// locks a username out after LoginFailureLimit failures, even when the
// correct password is then presented (credential-stuffing protection).
func TestAuthLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	_ = api
	if res, _ := registerUser(t, ts, "locky", "correct-password"); res.StatusCode != http.StatusCreated &&
		res.StatusCode != http.StatusConflict {
		t.Fatalf("register locky = %d, want 201", res.StatusCode)
	}

	for i := 0; i < LoginFailureLimit; i++ {
		res, _ := loginUser(t, ts, "locky", "wrong-password")
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}

	// Locked out: even the correct password is rejected with 429.
	res, _ := loginUser(t, ts, "locky", "correct-password")
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("login after lockout = %d, want 429", res.StatusCode)
	}
}

// TestAuthLoginUnknownUserCountsTowardThrottle (4.16): unknown usernames are
// indistinguishable from wrong passwords AND count toward the same lockout,
// so the endpoint cannot be used to enumerate usernames by lockout behavior.
func TestAuthLoginUnknownUserCountsTowardThrottle(t *testing.T) {
	api, ts := newAuthRequiredTestAPI(t)
	_ = api

	for i := 0; i < LoginFailureLimit; i++ {
		res, _ := loginUser(t, ts, "ghost-user", "whatever")
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unknown-user attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}
	res, _ := loginUser(t, ts, "ghost-user", "whatever")
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unknown-user attempt after limit = %d, want 429", res.StatusCode)
	}
}
