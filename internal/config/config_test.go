package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "test-token-0123456789abcdef"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:8787" || cfg.HTTP.Path != "/mcp" || !cfg.Send.OnlyExistingConversations || cfg.Audit.LogText {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadYAMLAndEnv(t *testing.T) {
	p := writeFile(t, "config.yaml", `
session_file: /tmp/test-session.json
log_level: debug
http:
  listen: "127.0.0.1:9000"
  path: /sms
auth:
  allowlist: ["127.0.0.1", "198.51.100.0/24"]
send:
  only_existing_conversations: false
  max_length: 500
audit:
  file: /tmp/test-audit.jsonl
  log_text: true
`, 0o600)
	cfg, err := Load(p, env(map[string]string{
		"SMS_MCP_LISTEN":          "127.0.0.1:9100",
		"SMS_MCP_SEND_MAX_LENGTH": "320",
		"SMS_MCP_ALLOWLIST":       "::1, 127.0.0.0/8",
		"SMS_MCP_TOKEN":           testToken,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:9100" || cfg.HTTP.Path != "/sms" || cfg.LogLevel != "debug" {
		t.Fatalf("http: %+v", cfg.HTTP)
	}
	if cfg.Send.MaxLength != 320 || cfg.Send.OnlyExistingConversations || !cfg.Audit.LogText {
		t.Fatalf("send/audit: %+v %+v", cfg.Send, cfg.Audit)
	}
	if len(cfg.Auth.Allowlist) != 2 || cfg.Auth.Allowlist[0] != "::1" {
		t.Fatalf("allowlist: %v", cfg.Auth.Allowlist)
	}
	if cfg.Auth.Token != testToken {
		t.Fatal("token not taken from env")
	}
}

func TestEmptyFileIsDefaults(t *testing.T) {
	p := writeFile(t, "empty.yaml", "", 0o600)
	if _, err := Load(p, env(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	p := writeFile(t, "bad.yaml", "bearer_token: nope\n", 0o600)
	if _, err := Load(p, env(nil)); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestBadEnv(t *testing.T) {
	if _, err := Load("", env(map[string]string{"SMS_MCP_SEND_ENABLED": "maybe"})); err == nil {
		t.Fatal("expected bool parse error")
	}
	if _, err := Load("", env(map[string]string{"SMS_MCP_MAX_MESSAGES": "lots"})); err == nil {
		t.Fatal("expected int parse error")
	}
}

func TestTokenFile(t *testing.T) {
	p := writeFile(t, "token", testToken+"\n", 0o600)
	cfg, err := Load("", env(map[string]string{"SMS_MCP_TOKEN_FILE": p}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Token != testToken {
		t.Fatal("token not read from file")
	}
	open := writeFile(t, "token-open", testToken, 0o644)
	if _, err := Load("", env(map[string]string{"SMS_MCP_TOKEN_FILE": open})); err == nil || !strings.Contains(err.Error(), "world") {
		t.Fatalf("expected world-readable error, got %v", err)
	}
	short := writeFile(t, "token-short", "short", 0o600)
	if _, err := Load("", env(map[string]string{"SMS_MCP_TOKEN_FILE": short})); err == nil {
		t.Fatal("expected short-token error")
	}
}

func TestNonLoopbackNeedsToken(t *testing.T) {
	if _, err := Load("", env(map[string]string{"SMS_MCP_LISTEN": "0.0.0.0:8787"})); err == nil {
		t.Fatal("expected error for public listen without token")
	}
	if _, err := Load("", env(map[string]string{"SMS_MCP_LISTEN": "0.0.0.0:8787", "SMS_MCP_TOKEN": testToken})); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("", env(map[string]string{"SMS_MCP_LISTEN": "0.0.0.0:8787", "SMS_MCP_ALLOW_UNAUTHENTICATED": "true"})); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("", env(map[string]string{"SMS_MCP_LISTEN": "[::1]:8787"})); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	for name, e := range map[string]map[string]string{
		"path":      {"SMS_MCP_PATH": "mcp"},
		"level":     {"SMS_MCP_LOG_LEVEL": "loud"},
		"allowlist": {"SMS_MCP_ALLOWLIST": "not-an-ip"},
		"maxlen":    {"SMS_MCP_SEND_MAX_LENGTH": "0"},
		"limits":    {"SMS_MCP_MAX_CONTACTS": "-1"},
	} {
		if _, err := Load("", env(e)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestParseAllowlist(t *testing.T) {
	p, err := ParseAllowlist([]string{"127.0.0.1", "198.51.100.7/24", "::1", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 3 || p[0].Bits() != 32 || p[1].String() != "198.51.100.0/24" || p[2].Bits() != 128 {
		t.Fatalf("got %v", p)
	}
	if _, err := ParseAllowlist([]string{"300.1.1.1"}); err == nil {
		t.Fatal("expected error")
	}
}
