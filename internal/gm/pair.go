package gm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// qrExpiry matches the bridge: a pairing QR code is refreshed every 30 s.
const qrExpiry = 30 * time.Second

// PairQR pairs a new browser session by QR code, the same flow as
// messages.google.com "Pair with QR code" and the bridge's QRLoginProcess:
// FetchConfig -> StartLogin (QR URL) -> RefreshPhoneRelay every 30 s ->
// PairCallback with the paired data.
//
// onQR is called with each QR payload (a URL that must be rendered as a QR
// code; it contains the session's encryption keys, so never log it).
func PairQR(ctx context.Context, log zerolog.Logger, onQR func(qr string) error) (*Session, error) {
	cli := libgm.NewClient(libgm.NewAuthData(), nil, log.With().Str("component", "libgm").Logger(), exhttp.SensibleClientSettings)
	cli.SetEventHandler(func(evt any) {
		log.Debug().Type("event_type", evt).Msg("pre-pairing event")
	})
	success := make(chan *gmproto.PairedData, 1)
	cb := func(data *gmproto.PairedData) {
		select {
		case success <- data:
		default:
		}
	}
	cli.PairCallback.Store(&cb)
	defer cli.Disconnect()

	if err := cli.FetchConfig(ctx); err != nil {
		return nil, fmt.Errorf("fetching Google Messages config: %w", err)
	}
	bgCtx, bgCancel := context.WithCancel(ctx)
	defer bgCancel()
	qr, err := cli.StartLogin(bgCtx)
	if err != nil {
		return nil, fmt.Errorf("starting QR login: %w", err)
	}
	if err := onQR(qr); err != nil {
		return nil, err
	}
	timer := time.NewTimer(qrExpiry)
	defer timer.Stop()
	for {
		select {
		case data := <-success:
			return &Session{
				PairedAt: time.Now().UTC(),
				PhoneID:  data.GetMobile().GetSourceID(),
				AuthData: cli.AuthData,
			}, nil
		case <-timer.C:
			qr, err = cli.RefreshPhoneRelay()
			if err != nil {
				return nil, fmt.Errorf("refreshing QR code: %w", err)
			}
			if err := onQR(qr); err != nil {
				return nil, err
			}
			timer.Reset(qrExpiry)
		case <-ctx.Done():
			return nil, fmt.Errorf("QR code was not scanned in time: %w", ctx.Err())
		}
	}
}

// RequiredGoogleCookies are the cookies the bridge requires for Google
// account pairing.
var RequiredGoogleCookies = []string{"SID", "HSID", "OSID", "SSID", "APISID", "SAPISID"}

// LoadGoogleCookies reads a JSON object of cookie name -> value.
func LoadGoogleCookies(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cookies map[string]string
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, fmt.Errorf("cookies file must be a JSON object of name to value: %w", err)
	}
	var missing []string
	for _, name := range RequiredGoogleCookies {
		if cookies[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("cookies file is missing %v", missing)
	}
	// Drop anything unexpected so the session file only stores what is used.
	allowed := append(slices.Clone(RequiredGoogleCookies), "__Secure-1PSIDTS")
	for name := range cookies {
		if !slices.Contains(allowed, name) {
			delete(cookies, name)
		}
	}
	return cookies, nil
}

// PairGoogle pairs using Google account cookies taken from a logged-in
// browser, the bridge's GoogleLoginProcess: FetchConfig -> StartGaiaPairing
// (returns an emoji) -> user taps the same emoji on the phone ->
// FinishGaiaPairing.
func PairGoogle(ctx context.Context, log zerolog.Logger, cookies map[string]string, onEmoji func(emoji, svgURL string)) (*Session, error) {
	ad := libgm.NewAuthData()
	ad.SetCookies(cookies)
	cli := libgm.NewClient(ad, nil, log.With().Str("component", "libgm").Logger(), exhttp.SensibleClientSettings)
	cli.SetEventHandler(func(evt any) {
		log.Debug().Type("event_type", evt).Msg("pre-pairing event")
	})
	defer cli.Disconnect()
	if err := cli.FetchConfig(ctx); err != nil {
		return nil, fmt.Errorf("fetching Google Messages config (cookies valid?): %w", err)
	}
	bgCtx, bgCancel := context.WithCancel(ctx)
	defer bgCancel()
	emoji, ps, err := cli.StartGaiaPairing(ctx, bgCtx)
	if err != nil {
		switch {
		case errors.Is(err, libgm.ErrNoDevicesFound):
			return nil, fmt.Errorf("no phone found: enable account pairing (Device pairing) in Google Messages: %w", err)
		case errors.Is(err, libgm.ErrPairingInitTimeout):
			return nil, fmt.Errorf("phone not responding: keep Google Messages open and online: %w", err)
		}
		return nil, fmt.Errorf("starting Google account pairing: %w", err)
	}
	onEmoji(emoji, libgm.GetEmojiSVG(emoji))
	phoneID, err := cli.FinishGaiaPairing(ctx, ps)
	if err != nil {
		return nil, fmt.Errorf("finishing Google account pairing: %w", err)
	}
	return &Session{PairedAt: time.Now().UTC(), PhoneID: phoneID, AuthData: cli.AuthData}, nil
}

// VerifySession connects once with a freshly paired session and lists one
// conversation, to confirm the phone accepted the pairing. Session updates
// (refreshed tokens) end up in sess.AuthData, so save it afterwards.
func VerifySession(ctx context.Context, log zerolog.Logger, sess *Session) error {
	cli := libgm.NewClient(sess.AuthData, nil, log.With().Str("component", "libgm").Logger(), exhttp.SensibleClientSettings)
	cli.SetEventHandler(func(evt any) {
		log.Trace().Type("event_type", evt).Msg("verify event")
	})
	defer cli.Disconnect()
	if err := cli.FetchConfig(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to fetch config")
	}
	if err := cli.Connect(ctx); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	// Give the long poll a moment to come up before the first request.
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := cli.ListConversations(ctx, &gmproto.ListConversationsRequest{
		Count:  1,
		Folder: gmproto.ListConversationsRequest_INBOX,
	}); err != nil {
		return fmt.Errorf("listing conversations: %w", err)
	}
	return nil
}
