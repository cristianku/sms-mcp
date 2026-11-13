package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cristianku/sms-mcp/internal/httpauth"
)

// HTTPOptions configures the HTTP handler.
type HTTPOptions struct {
	Path                       string
	Token                      string
	Allowlist                  []netip.Prefix
	DisableLocalhostProtection bool
	// Stateless enables the SDK's stateless mode: no Mcp-Session-Id, POST
	// only, and support for the 2026-07-28 protocol (server/discover).
	Stateless bool
	// Health returns the connection state shown on /healthz.
	Health func() string
}

// NewHTTPHandler serves the MCP streamable HTTP endpoint at opts.Path (behind
// the bearer token) and a minimal /healthz. Everything is behind the IP
// allowlist.
func NewHTTPHandler(srv *mcp.Server, opts HTTPOptions) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: opts.DisableLocalhostProtection,
		Stateless:                  opts.Stateless,
	})
	mux := http.NewServeMux()
	mux.Handle(opts.Path, httpauth.Bearer(opts.Token, mcpHandler))
	// No OAuth: answer metadata discovery with a JSON 404 so clients that
	// probe /.well-known/oauth-protected-resource* get parseable JSON instead
	// of the mux's plain-text "404 page not found".
	mux.HandleFunc("/.well-known/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}` + "\n"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		state := "unknown"
		if opts.Health != nil {
			state = opts.Health()
		}
		w.Header().Set("Content-Type", "application/json")
		if state != "connected" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": state})
	})
	return httpauth.Allowlist(opts.Allowlist, securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
