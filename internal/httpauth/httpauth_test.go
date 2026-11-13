package httpauth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func TestBearer(t *testing.T) {
	h := Bearer("test-token-0123456789abcdef", ok)
	for _, c := range []struct {
		header string
		want   int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"Basic dGVzdA==", http.StatusUnauthorized},
		{"Bearer", http.StatusUnauthorized},
		{"Bearer test-token-0123456789abcdef", http.StatusNoContent},
		{"bearer test-token-0123456789abcdef", http.StatusNoContent},
	} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("header %q: got %d want %d", c.header, w.Code, c.want)
		}
		if w.Code == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
			t.Error("missing WWW-Authenticate")
		}
	}
	// Empty token disables the check.
	w := httptest.NewRecorder()
	Bearer("", ok).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusNoContent {
		t.Fatal("empty token should pass through")
	}
}

func TestAllowlist(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := map[string]bool{
		"127.0.0.1:1234":        true,
		"[::ffff:127.0.0.1]:80": true,
		"[fd12::1]:443":         true,
		"[fe80::1%eth0]:443":    false,
		"203.0.113.5:80":        false,
		"garbage":               false,
	}
	for addr, want := range cases {
		if got := Allowed(prefixes, addr); got != want {
			t.Errorf("Allowed(%q)=%v want %v", addr, got, want)
		}
	}
	h := Allowlist(prefixes, ok)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:80"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("X-Forwarded-For must be ignored, got %d", w.Code)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("got %d", w.Code)
	}
}
