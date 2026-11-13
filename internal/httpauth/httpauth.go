// Package httpauth provides the bearer-token and IP allowlist middleware that
// guard the MCP endpoint.
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Bearer requires "Authorization: Bearer <token>". An empty token disables the
// check. The comparison is constant-time and the presented token is never
// logged.
func Bearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerToken(r.Header.Get("Authorization"))
		gotSum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(want[:], gotSum[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sms-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(h string) (string, bool) {
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// Allowlist only lets through requests whose TCP peer address (RemoteAddr) is
// inside one of the prefixes. An empty list disables the check.
// X-Forwarded-For is deliberately ignored: behind a reverse proxy, allowlist
// the proxy and filter clients at the proxy.
func Allowlist(prefixes []netip.Prefix, next http.Handler) http.Handler {
	if len(prefixes) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Allowed(prefixes, r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Allowed reports whether remoteAddr ("ip:port" or bare IP) is in prefixes.
func Allowed(prefixes []netip.Prefix, remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	// Strip an IPv6 zone, e.g. fe80::1%eth0.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
