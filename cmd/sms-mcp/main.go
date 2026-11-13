// Command sms-mcp pairs with Google Messages on an Android phone and exposes
// SMS/RCS as MCP tools over streamable HTTP.
//
// It uses libgm from mautrix-gmessages by Tulir Asokan (AGPL-3.0).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/cristianku/sms-mcp/internal/audit"
	"github.com/cristianku/sms-mcp/internal/config"
	"github.com/cristianku/sms-mcp/internal/gm"
	"github.com/cristianku/sms-mcp/internal/mcpserver"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `sms-mcp - SMS/RCS for MCP clients via Google Messages (unofficial)

Usage:
  sms-mcp pair   [-config FILE] [-google-cookies FILE] [-force] [-invert]
  sms-mcp serve  [-config FILE]
  sms-mcp check-config [-config FILE]
  sms-mcp version

Configuration is read from the YAML file given with -config (or
SMS_MCP_CONFIG) and overridden by SMS_MCP_* environment variables.
See config.example.yaml and README.md.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "pair":
		err = cmdPair(args)
	case "serve":
		err = cmdServe(args)
	case "check-config":
		err = cmdCheckConfig(args)
	case "version", "-version", "--version":
		fmt.Println("sms-mcp", version)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (*config.Config, error) {
	path := fs.String("config", os.Getenv("SMS_MCP_CONFIG"), "path to YAML config file")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return config.Load(*path, os.Getenv)
}

func newLogger(level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		Level(lvl).With().Timestamp().Logger()
}

func cmdCheckConfig(args []string) error {
	cfg, err := loadConfig(flag.NewFlagSet("check-config", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	fmt.Printf("config OK: listen=%s path=%s session_file=%s send_enabled=%v only_existing=%v token_set=%v allowlist=%v\n",
		cfg.HTTP.Listen, cfg.HTTP.Path, cfg.SessionFile, cfg.Send.Enabled, cfg.Send.OnlyExistingConversations,
		cfg.Auth.Token != "", cfg.Auth.Allowlist)
	return nil
}

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	cookiesPath := fs.String("google-cookies", "", "pair with Google account cookies (JSON file) instead of a QR code")
	force := fs.Bool("force", false, "replace an existing session file")
	invert := fs.Bool("invert", false, "invert the terminal QR code (for light terminal backgrounds)")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	store := &gm.SessionStore{Path: cfg.SessionFile, Warn: func(m string) { log.Warn().Msg(m) }}

	if _, err := os.Stat(cfg.SessionFile); err == nil && !*force {
		return fmt.Errorf("session file %s already exists; use -force to replace it (and remove the old device in Google Messages > Device pairing)", cfg.SessionFile)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SessionFile), 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.Pairing.Timeout)
	defer cancel()

	var sess *gm.Session
	if *cookiesPath != "" {
		cookies, err := gm.LoadGoogleCookies(*cookiesPath)
		if err != nil {
			return err
		}
		sess, err = gm.PairGoogle(ctx, log, cookies, func(emoji, svgURL string) {
			fmt.Printf("\nOn your phone, Google Messages will ask you to confirm pairing.\nTap this emoji: %s\n(image: %s)\n\n", emoji, svgURL)
		})
		if err != nil {
			return err
		}
	} else {
		png := cfg.Pairing.QRPNG
		if png != "" {
			defer func() { _ = os.Remove(png) }()
		}
		fmt.Println("Open Google Messages on your phone > profile picture > Device pairing > QR code scanner, and scan:")
		sess, err = gm.PairQR(ctx, log, func(qr string) error {
			fmt.Println()
			if err := renderQR(os.Stdout, qr, *invert); err != nil {
				return err
			}
			if png != "" {
				if err := writeQRPNG(png, qr); err != nil {
					return fmt.Errorf("writing QR PNG: %w", err)
				}
				fmt.Printf("QR code also written to %s (mode 0600, deleted after pairing). It refreshes every 30 s.\n", png)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	fmt.Printf("Paired. Session saved to %s (mode 0600).\n", cfg.SessionFile)

	// Like the bridge, wait before the first connect so the phone stores the pairing.
	time.Sleep(2 * time.Second)
	vctx, vcancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer vcancel()
	if err := gm.VerifySession(vctx, log, sess); err != nil {
		log.Warn().Err(err).Msg("pairing saved, but the verification connect failed; `sms-mcp serve` will retry")
	} else {
		fmt.Println("Verified: the phone answered a request.")
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	return nil
}

func cmdServe(args []string) error {
	cfg, err := loadConfig(flag.NewFlagSet("serve", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	warn := func(m string) { log.Warn().Msg(m) }

	store := &gm.SessionStore{Path: cfg.SessionFile, Warn: warn}
	svc, err := gm.NewService(store, log)
	if err != nil {
		return err
	}

	var auditor mcpserver.Auditor
	if cfg.Send.Enabled {
		al, err := audit.Open(cfg.Audit.File, cfg.Audit.LogText, warn)
		if err != nil {
			return err
		}
		defer func() { _ = al.Close() }()
		auditor = al
	}
	allow, err := config.ParseAllowlist(cfg.Auth.Allowlist)
	if err != nil {
		return err
	}
	if cfg.Auth.Token == "" {
		log.Warn().Msg("no bearer token configured: anyone who can reach the listener can read and send SMS")
	}

	tools := mcpserver.NewTools(svc, auditor, mcpserver.Options{
		SendEnabled:       cfg.Send.Enabled,
		OnlyExisting:      cfg.Send.OnlyExistingConversations,
		MaxTextLength:     cfg.Send.MaxLength,
		ExistingScanLimit: cfg.Send.ExistingScanLimit,
		MaxConversations:  cfg.Limits.MaxConversations,
		MaxMessages:       cfg.Limits.MaxMessages,
		MaxContacts:       cfg.Limits.MaxContacts,
		Version:           version,
	})
	handler := mcpserver.NewHTTPHandler(mcpserver.NewServer(tools), mcpserver.HTTPOptions{
		Path:                       cfg.HTTP.Path,
		Token:                      cfg.Auth.Token,
		Allowlist:                  allow,
		DisableLocalhostProtection: cfg.HTTP.DisableLocalhostProtection,
		Health:                     func() string { return string(svc.State()) },
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	httpErr := make(chan error, 1)
	go func() { httpErr <- srv.Serve(ln) }()
	log.Info().Str("addr", ln.Addr().String()).Str("path", cfg.HTTP.Path).
		Bool("send_enabled", cfg.Send.Enabled).Bool("only_existing", cfg.Send.OnlyExistingConversations).
		Bool("token", cfg.Auth.Token != "").Int("allowlist_entries", len(allow)).
		Msg("MCP server listening")

	svcDone := make(chan error, 1)
	go func() { svcDone <- svc.Run(ctx) }()

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-httpErr:
		runErr = err
		stop()
	case err := <-svcDone:
		svcDone <- err
		if errors.Is(err, gm.ErrLoggedOut) {
			// Keep serving so clients get a clear error; /healthz reports logged_out.
			log.Error().Msg("session is no longer valid; tools will fail until you re-pair and restart")
			select {
			case <-ctx.Done():
			case runErr = <-httpErr:
			}
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	stop()
	<-svcDone
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}
