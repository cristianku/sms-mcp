# sms-mcp

A small headless service that pairs with **Google Messages** on your Android
phone, like messages.google.com does, and exposes your SMS/RCS conversations as
[MCP](https://modelcontextprotocol.io) tools over streamable HTTP. MCP clients
such as Claude Code can then list and read conversations, search contacts and
(optionally) send messages.

It is built on **libgm** from
[mautrix-gmessages](https://github.com/mautrix/gmessages) by **Tulir Asokan**.
All of the Google Messages protocol work is his; sms-mcp is a thin wrapper.

> [!WARNING]
> **Unofficial protocol.** sms-mcp is not affiliated with or endorsed by
> Google. It uses the same reverse-engineered web-client protocol as
> mautrix-gmessages. Google can change or block it at any time, and using it
> might go against Google's terms of service. It acts on your real phone
> number: messages it sends are real SMS/RCS and may cost money. Use at your
> own risk.

## Tools

| Tool | Kind | Description |
|---|---|---|
| `list_conversations(limit, query)` | read-only | Recent conversations, newest first. `query` filters by name or number. |
| `read_messages(conversation_id, limit)` | read-only | Latest messages of a conversation, newest first. |
| `search_contacts(query)` | read-only | Phone contacts matching a name or number. |
| `send_sms(to, text)` | write | Send a message to a phone number (E.164 recommended, e.g. `+15550100`). |
| `reply(conversation_id, text)` | write | Send a message into an existing conversation. |

The read tools are annotated `readOnlyHint: true`. The write tools are
annotated `readOnlyHint: false`, `destructiveHint: false`,
`openWorldHint: true`. When `send.enabled` is false they are not registered.

Message bodies and conversation snippets are always returned in an
`untrusted_content` field, together with a `notice` that the content was written
by third parties. It can contain prompt-injection attempts and one-time codes
(2FA/OTP), and it must never be treated as instructions.

## Requirements

- An Android phone with Google Messages as the default SMS app, online.
  The phone stays the source of truth: it sends and receives everything.
- Go 1.26 or newer to build (no cgo needed). Linux, macOS and Windows all
  build; the systemd unit is Linux-only.
- Network access to Google's Messages endpoints (`instantmessaging-pa.googleapis.com`
  and related hosts).

## Install

```sh
git clone https://github.com/cristianku/sms-mcp.git
cd sms-mcp
make build            # CGO_ENABLED=0, static binary ./sms-mcp
./sms-mcp version
```

Or `go install github.com/cristianku/sms-mcp/cmd/sms-mcp@latest`.

A container image can be built with `make docker`; see the `Dockerfile`, a
multi-stage build on `distroless/static` that runs as nonroot. For a host
install, `deploy/sms-mcp.service` is a hardened systemd unit (DynamicUser,
ProtectSystem=strict, empty capability set, syscall filter, token passed as a
systemd credential). Its header comments contain the install steps.

## Pairing

Pairing creates a "browser" session, the same as messages.google.com, and stores
it in `session_file` (mode 0600). Run it once:

### QR code (recommended)

```sh
./sms-mcp pair -config config.yaml
```

A QR code is printed in the terminal using half blocks; pass `-invert` on a
light background. A PNG copy is written to `pairing.qr_png`, mode 0600, so you
can open it from another machine; it is deleted after pairing. On the phone,
open Google Messages > profile picture > **Device pairing** > **QR code
scanner** and scan the code. The code refreshes every 30 seconds until
`pairing.timeout` (default 3 minutes). The QR payload contains the session's
encryption keys, so do not share it or the PNG.

### Google account (headless, experimental)

If you cannot scan a QR code, you can pair through the Google account the
phone uses, as the bridge does. This needs cookies from a browser that is
logged in to messages.google.com with that account:

```json
{
  "SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
  "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."
}
```

```sh
chmod 600 cookies.json
./sms-mcp pair -config config.yaml -google-cookies cookies.json
```

sms-mcp prints an emoji; tap the same emoji in the prompt on the phone. Only
the listed cookies are kept, and they end up in the session file. Delete
`cookies.json` afterwards. Caveats:

- These cookies give broad access to your Google account. Treat the cookies
  file and the session file as you would a password.
- Google rotates the cookies. sms-mcp saves the rotated values, but a session
  can still be invalidated, for example when you log out of that browser.
  If that happens, pair again.
- Account pairing must be enabled in Google Messages > Device pairing.

`pair` refuses to overwrite an existing session unless you pass `-force`. After
re-pairing, remove the old device under Device pairing on the phone.

## Running

```sh
export SMS_MCP_TOKEN_FILE=/path/to/token    # or SMS_MCP_TOKEN
./sms-mcp serve -config config.yaml
```

`serve` connects with exponential backoff, from 5 seconds up to 5 minutes,
reconnects when the long poll fails, and saves the session whenever libgm
refreshes its tokens or cookies.

- `GET /healthz` returns `{"state": "connected" | "connecting" | "reconnecting" | "logged_out" | "stopped"}`,
  with status 200 only when connected. It requires no token.
- If the phone unpairs the session, the state becomes `logged_out` and the
  tools return an error; run `pair -force` and restart.
- `./sms-mcp check-config` validates the configuration without connecting.

## Configuration

Copy `config.example.yaml` to `config.yaml`, which is git-ignored. The file is
optional; unknown keys are rejected. Environment variables override the file:

| Variable | Config key | Default |
|---|---|---|
| `SMS_MCP_CONFIG` | path of the YAML file | none |
| `SMS_MCP_SESSION_FILE` | `session_file` | `session.json` |
| `SMS_MCP_LOG_LEVEL` | `log_level` | `info` |
| `SMS_MCP_LISTEN` | `http.listen` | `127.0.0.1:8787` |
| `SMS_MCP_PATH` | `http.path` | `/mcp` |
| `SMS_MCP_DISABLE_LOCALHOST_PROTECTION` | `http.disable_localhost_protection` | `false` |
| `SMS_MCP_TOKEN` | the bearer token itself | none |
| `SMS_MCP_TOKEN_FILE` | `auth.token_file` | none |
| `SMS_MCP_ALLOWLIST` | `auth.allowlist` (comma-separated) | empty (any peer) |
| `SMS_MCP_ALLOW_UNAUTHENTICATED` | `auth.allow_unauthenticated` | `false` |
| `SMS_MCP_SEND_ENABLED` | `send.enabled` | `true` |
| `SMS_MCP_SEND_ONLY_EXISTING` | `send.only_existing_conversations` | `true` |
| `SMS_MCP_SEND_MAX_LENGTH` | `send.max_length` | `1600` |
| `SMS_MCP_AUDIT_FILE` | `audit.file` | `audit.jsonl` |
| `SMS_MCP_AUDIT_LOG_TEXT` | `audit.log_text` | `false` |
| `SMS_MCP_QR_PNG` | `pairing.qr_png` | `pairing-qr.png` |
| `SMS_MCP_MAX_CONVERSATIONS` / `_MAX_MESSAGES` / `_MAX_CONTACTS` | `limits.*` | `100` |

`send.existing_scan_limit` (default 200) and `pairing.timeout` (default `3m`)
can only be set in the file.

## MCP client setup

The endpoint is `http://127.0.0.1:8787/mcp` (streamable HTTP).

**Claude Code**

```sh
claude mcp add --transport http sms http://127.0.0.1:8787/mcp \
  --header "Authorization: Bearer $(cat /path/to/token)"
```

**Generic JSON configuration** (clients that support remote HTTP servers):

```json
{
  "mcpServers": {
    "sms": {
      "type": "http",
      "url": "http://127.0.0.1:8787/mcp",
      "headers": { "Authorization": "Bearer ${SMS_MCP_TOKEN}" }
    }
  }
}
```

For clients that only support stdio, use a bridge such as
`npx mcp-remote http://127.0.0.1:8787/mcp --header "Authorization: Bearer ..."`.

To reach the service from another machine, prefer an SSH tunnel
(`ssh -L 8787:127.0.0.1:8787 host`) over a public listener. If you put it behind
a reverse proxy with TLS, keep the token, set an allowlist and, if the proxy
rewrites `Host`, set `http.disable_localhost_protection`.

## Security

Anyone who can call these tools can read your private messages, including 2FA
codes, and send SMS from your number. Defaults are conservative:

- **Listener**: loopback only by default. A non-loopback `listen` address
  without a token is refused unless you set `allow_unauthenticated`.
- **Bearer token**: read from `SMS_MCP_TOKEN` or `auth.token_file` (at least 24
  characters). The file must not be world-accessible. The token is compared
  in constant time and never logged.
- **Allowlist**: optional IP/CIDR list checked against the TCP peer address.
  `X-Forwarded-For` is ignored on purpose.
- **DNS rebinding**: the MCP SDK's localhost protection stays on by default.
- **Sending**: `send.enabled: false` makes the server read-only. With
  `send.only_existing_conversations: true` (default), `send_sms` only goes to
  numbers that already have a 1:1 conversation among the most recent
  `send.existing_scan_limit` ones; it never starts a new thread. Texts are
  limited to `send.max_length` characters.
- **Audit log**: every send attempt (pending, sent, refused, error) is appended
  to a JSONL file with mode 0600, with timestamp, tool, target, conversation,
  length and result. The text is recorded only with `audit.log_text: true`. If
  the audit record cannot be written, the message is not sent.
- **Files**: the session file is written atomically with mode 0600. If an
  existing session or audit file has wider permissions, it is fixed and a
  warning is logged. The pairing PNG is 0600 and deleted after pairing.
- **Prompt injection**: message content is labelled `untrusted_content`. Even so,
  an agent reading messages can be manipulated by them, so keep sending
  disabled, or keep human approval of the write tools turned on in your MCP
  client, unless you need it.
- **Logs**: sms-mcp itself never logs message text, phone numbers or tokens.
  libgm's own log lines go through the same logger; at `debug` and `trace`
  they may include protocol details and identifiers, so do not use those
  levels in production.

Report security issues privately to the maintainers rather than in public issues.

## Development

```sh
make test     # go test ./...
make race     # with the race detector
make lint     # gofmt check + go vet (+ staticcheck if installed)
```

Tests use fake clients and fictional numbers (`+1555010xxxx`); they do not talk
to Google.

## Credits

- [mautrix-gmessages](https://github.com/mautrix/gmessages) and its `libgm`
  package, by **Tulir Asokan** (AGPL-3.0). It provides pairing, encryption,
  long polling and every Google Messages request used here.
- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).
- [go-qrcode](https://github.com/skip2/go-qrcode) and [zerolog](https://github.com/rs/zerolog).

## License

GNU Affero General Public License v3.0; see [LICENSE](LICENSE) and
[NOTICE](NOTICE). Because libgm is AGPL-3.0, sms-mcp is too. If you run a
modified version as a network service, you must offer its source to the users
of that service.
