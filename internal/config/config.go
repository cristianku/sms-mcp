// Package config loads the sms-mcp configuration from a YAML file and applies
// SMS_MCP_* environment variable overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EnvPrefix is the prefix of every environment variable read by sms-mcp.
const EnvPrefix = "SMS_MCP_"

// Config is the full service configuration.
type Config struct {
	// SessionFile holds the paired Google Messages session (encryption keys
	// and tokens). It is created with mode 0600.
	SessionFile string `yaml:"session_file"`
	// LogLevel is one of trace, debug, info, warn, error.
	LogLevel string `yaml:"log_level"`

	HTTP    HTTPConfig    `yaml:"http"`
	Auth    AuthConfig    `yaml:"auth"`
	Send    SendConfig    `yaml:"send"`
	Audit   AuditConfig   `yaml:"audit"`
	Pairing PairingConfig `yaml:"pairing"`
	Limits  LimitsConfig  `yaml:"limits"`
}

// HTTPConfig configures the MCP HTTP listener.
type HTTPConfig struct {
	Listen string `yaml:"listen"`
	Path   string `yaml:"path"`
	// DisableLocalhostProtection turns off the MCP SDK's DNS-rebinding guard,
	// which rejects requests arriving on a loopback address with a
	// non-localhost Host header. Needed only behind a local reverse proxy that
	// does not preserve the Host header.
	DisableLocalhostProtection bool `yaml:"disable_localhost_protection"`
	// Stateless serves every POST with a fresh temporary MCP session and no
	// Mcp-Session-Id. Clients on protocol 2026-07-28 (server/discover, used
	// by ChatGPT) are only accepted in this mode. GET and DELETE return 405.
	Stateless bool `yaml:"stateless"`
}

// AuthConfig configures request authentication.
type AuthConfig struct {
	// TokenFile is a file containing the bearer token. The token itself can
	// also be given via the SMS_MCP_TOKEN environment variable. It is never
	// read from the YAML file directly.
	TokenFile string `yaml:"token_file"`
	// Allowlist is a list of IPs or CIDR prefixes allowed to connect. Empty
	// means any address.
	Allowlist []string `yaml:"allowlist"`
	// AllowUnauthenticated permits running without a bearer token on a
	// non-loopback listen address. Off by default.
	AllowUnauthenticated bool `yaml:"allow_unauthenticated"`

	// Token is resolved at load time from SMS_MCP_TOKEN or TokenFile.
	Token string `yaml:"-"`
}

// SendConfig controls the write tools.
type SendConfig struct {
	// Enabled registers send_sms and reply. When false the server is read-only.
	Enabled bool `yaml:"enabled"`
	// OnlyExistingConversations refuses send_sms to numbers that have no
	// existing conversation on the phone.
	OnlyExistingConversations bool `yaml:"only_existing_conversations"`
	// MaxLength is the maximum message length in characters.
	MaxLength int `yaml:"max_length"`
	// ExistingScanLimit is how many recent conversations are scanned to decide
	// whether a number already has a conversation.
	ExistingScanLimit int `yaml:"existing_scan_limit"`
}

// AuditConfig configures the append-only send audit log.
type AuditConfig struct {
	File    string `yaml:"file"`
	LogText bool   `yaml:"log_text"`
}

// PairingConfig configures the pair subcommand.
type PairingConfig struct {
	// QRPNG is where the pairing QR code PNG is written. It is deleted after
	// pairing. Empty disables the PNG.
	QRPNG string `yaml:"qr_png"`
	// Timeout is how long to wait for the phone to scan the QR code.
	Timeout time.Duration `yaml:"timeout"`
}

// LimitsConfig caps how much data a single tool call may return.
type LimitsConfig struct {
	MaxConversations int `yaml:"max_conversations"`
	MaxMessages      int `yaml:"max_messages"`
	MaxContacts      int `yaml:"max_contacts"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		SessionFile: "session.json",
		LogLevel:    "info",
		HTTP: HTTPConfig{
			Listen:    "127.0.0.1:8787",
			Path:      "/mcp",
			Stateless: true,
		},
		Send: SendConfig{
			Enabled:                   true,
			OnlyExistingConversations: true,
			MaxLength:                 1600,
			ExistingScanLimit:         200,
		},
		Audit: AuditConfig{
			File: "audit.jsonl",
		},
		Pairing: PairingConfig{
			QRPNG:   "pairing-qr.png",
			Timeout: 3 * time.Minute,
		},
		Limits: LimitsConfig{
			MaxConversations: 100,
			MaxMessages:      100,
			MaxContacts:      100,
		},
	}
}

// Load reads the YAML file at path (optional: empty path means defaults only),
// applies environment overrides from getenv and validates the result.
func Load(path string, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		// An empty file decodes to io.EOF: treat it as "no settings".
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return nil, err
	}
	if err := resolveToken(&cfg, getenv); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	str := func(name string, dst *string) {
		if v := getenv(EnvPrefix + name); v != "" {
			*dst = v
		}
	}
	boolean := func(name string, dst *bool) error {
		if v := getenv(EnvPrefix + name); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("%s%s: %w", EnvPrefix, name, err)
			}
			*dst = b
		}
		return nil
	}
	integer := func(name string, dst *int) error {
		if v := getenv(EnvPrefix + name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s%s: %w", EnvPrefix, name, err)
			}
			*dst = n
		}
		return nil
	}

	str("SESSION_FILE", &cfg.SessionFile)
	str("LOG_LEVEL", &cfg.LogLevel)
	str("LISTEN", &cfg.HTTP.Listen)
	str("PATH", &cfg.HTTP.Path)
	str("TOKEN_FILE", &cfg.Auth.TokenFile)
	str("AUDIT_FILE", &cfg.Audit.File)
	str("QR_PNG", &cfg.Pairing.QRPNG)
	if v := getenv(EnvPrefix + "ALLOWLIST"); v != "" {
		cfg.Auth.Allowlist = nil
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.Auth.Allowlist = append(cfg.Auth.Allowlist, p)
			}
		}
	}
	for name, dst := range map[string]*bool{
		"SEND_ENABLED":                 &cfg.Send.Enabled,
		"SEND_ONLY_EXISTING":           &cfg.Send.OnlyExistingConversations,
		"AUDIT_LOG_TEXT":               &cfg.Audit.LogText,
		"DISABLE_LOCALHOST_PROTECTION": &cfg.HTTP.DisableLocalhostProtection,
		"ALLOW_UNAUTHENTICATED":        &cfg.Auth.AllowUnauthenticated,
		"STATELESS":                    &cfg.HTTP.Stateless,
	} {
		if err := boolean(name, dst); err != nil {
			return err
		}
	}
	for name, dst := range map[string]*int{
		"SEND_MAX_LENGTH":   &cfg.Send.MaxLength,
		"MAX_CONVERSATIONS": &cfg.Limits.MaxConversations,
		"MAX_MESSAGES":      &cfg.Limits.MaxMessages,
		"MAX_CONTACTS":      &cfg.Limits.MaxContacts,
	} {
		if err := integer(name, dst); err != nil {
			return err
		}
	}
	return nil
}

// MinTokenLength is the minimum accepted bearer token length.
const MinTokenLength = 24

func resolveToken(cfg *Config, getenv func(string) string) error {
	tok := getenv(EnvPrefix + "TOKEN")
	if tok == "" && cfg.Auth.TokenFile != "" {
		st, err := os.Stat(cfg.Auth.TokenFile)
		if err != nil {
			return fmt.Errorf("token file: %w", err)
		}
		if st.Mode().Perm()&0o007 != 0 {
			return fmt.Errorf("token file %s is world-accessible (mode %o); chmod 600 it", cfg.Auth.TokenFile, st.Mode().Perm())
		}
		data, err := os.ReadFile(cfg.Auth.TokenFile)
		if err != nil {
			return fmt.Errorf("token file: %w", err)
		}
		tok = strings.TrimSpace(string(data))
		if tok == "" {
			return fmt.Errorf("token file %s is empty", cfg.Auth.TokenFile)
		}
	}
	if tok != "" && len(tok) < MinTokenLength {
		return fmt.Errorf("bearer token is too short (%d chars, need at least %d)", len(tok), MinTokenLength)
	}
	cfg.Auth.Token = tok
	return nil
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	var errs []error
	if c.SessionFile == "" {
		errs = append(errs, errors.New("session_file must be set"))
	}
	switch c.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level %q is not one of trace, debug, info, warn, error", c.LogLevel))
	}
	if c.HTTP.Listen == "" {
		errs = append(errs, errors.New("http.listen must be set"))
	}
	if !strings.HasPrefix(c.HTTP.Path, "/") {
		errs = append(errs, fmt.Errorf("http.path %q must start with /", c.HTTP.Path))
	}
	if _, err := ParseAllowlist(c.Auth.Allowlist); err != nil {
		errs = append(errs, err)
	}
	if c.Send.MaxLength <= 0 || c.Send.MaxLength > 10000 {
		errs = append(errs, fmt.Errorf("send.max_length must be between 1 and 10000"))
	}
	if c.Send.ExistingScanLimit <= 0 {
		errs = append(errs, fmt.Errorf("send.existing_scan_limit must be positive"))
	}
	if c.Send.Enabled && c.Audit.File == "" {
		errs = append(errs, errors.New("audit.file must be set when sending is enabled"))
	}
	if c.Limits.MaxConversations <= 0 || c.Limits.MaxMessages <= 0 || c.Limits.MaxContacts <= 0 {
		errs = append(errs, errors.New("limits must be positive"))
	}
	if c.Auth.Token == "" && !c.Auth.AllowUnauthenticated && !IsLoopbackListen(c.HTTP.Listen) {
		errs = append(errs, fmt.Errorf("http.listen %q is not a loopback address and no bearer token is set: "+
			"set SMS_MCP_TOKEN or auth.token_file (or auth.allow_unauthenticated: true if you really mean it)", c.HTTP.Listen))
	}
	if c.Pairing.Timeout <= 0 {
		errs = append(errs, errors.New("pairing.timeout must be positive"))
	}
	return errors.Join(errs...)
}

// ParseAllowlist parses IPs and CIDR prefixes. A bare IP becomes a /32 or /128.
func ParseAllowlist(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("allowlist entry %q: %w", e, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("allowlist entry %q: %w", e, err)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// IsLoopbackListen reports whether a listen address only binds loopback.
func IsLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}
