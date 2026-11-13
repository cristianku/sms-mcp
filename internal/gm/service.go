package gm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/util"
)

// State is the connection state of the Service.
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateLoggedOut    State = "logged_out" // unpaired or credentials rejected: re-pair needed
	StateStopped      State = "stopped"
)

// ErrLoggedOut is returned once the phone has revoked the session.
var ErrLoggedOut = errors.New("the Google Messages session was revoked or rejected: run `sms-mcp pair` again")

// Backoff settings for reconnects.
const (
	backoffMin = 5 * time.Second
	backoffMax = 5 * time.Minute
	// sessionSaveInterval is how often the session is re-saved if it changed
	// (libgm rotates Google-account cookies in place).
	sessionSaveInterval = 10 * time.Minute
	requestTimeout      = 60 * time.Second
)

// Service owns a libgm client for a paired session, keeps it connected and
// persists session updates. It implements Client.
type Service struct {
	log   zerolog.Logger
	store *SessionStore
	sess  *Session

	mu  sync.Mutex
	cli *libgm.Client

	state     atomic.Value // State
	lastSaved []byte

	reconnectCh chan struct{}
	saveCh      chan struct{}
	loggedOutCh chan struct{}

	simsMu sync.RWMutex
	sims   map[string]*gmproto.SIMCard // by SIM participant ID
}

var _ Client = (*Service)(nil)

// NewService loads the session from store. It does not connect yet.
func NewService(store *SessionStore, log zerolog.Logger) (*Service, error) {
	sess, err := store.Load()
	if err != nil {
		return nil, err
	}
	s := &Service{
		log:         log,
		store:       store,
		sess:        sess,
		reconnectCh: make(chan struct{}, 1),
		saveCh:      make(chan struct{}, 1),
		loggedOutCh: make(chan struct{}, 1),
		sims:        make(map[string]*gmproto.SIMCard),
	}
	s.lastSaved, _ = Marshal(sess)
	s.state.Store(StateConnecting)
	return s, nil
}

// State returns the current connection state.
func (s *Service) State() State { return s.state.Load().(State) }

func (s *Service) newClient() *libgm.Client {
	cli := libgm.NewClient(s.sess.AuthData, nil, s.log.With().Str("component", "libgm").Logger(), exhttp.SensibleClientSettings)
	cli.SetEventHandler(s.handleEvent)
	return cli
}

// Run connects and keeps the session connected until ctx is cancelled,
// reconnecting with exponential backoff. It returns ErrLoggedOut if the phone
// revokes the pairing.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	s.cli = s.newClient()
	s.mu.Unlock()
	defer s.shutdown()

	saveTicker := time.NewTicker(sessionSaveInterval)
	defer saveTicker.Stop()

	attempt := 0
	connectNow := time.NewTimer(0)
	defer connectNow.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.loggedOutCh:
			s.state.Store(StateLoggedOut)
			s.log.Error().Msg("session revoked or rejected by Google/phone: pair again with `sms-mcp pair`")
			return ErrLoggedOut
		case <-s.saveCh:
			s.save()
		case <-saveTicker.C:
			s.save()
		case <-s.reconnectCh:
			s.state.Store(StateReconnecting)
			s.cli.Disconnect()
			d := Backoff(attempt)
			attempt++
			s.log.Warn().Dur("retry_in", d).Msg("connection lost, reconnecting")
			connectNow.Reset(d)
		case <-connectNow.C:
			err := s.connect(ctx)
			switch {
			case err == nil:
				attempt = 0
				s.state.Store(StateConnected)
				s.log.Info().Msg("connected to Google Messages")
				s.save()
			case isCredentialError(err):
				s.signalLoggedOut()
			default:
				d := Backoff(attempt)
				attempt++
				s.state.Store(StateReconnecting)
				s.log.Warn().Err(err).Dur("retry_in", d).Msg("connect failed")
				connectNow.Reset(d)
			}
		}
	}
}

func (s *Service) connect(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := s.cli.FetchConfig(cctx); err != nil {
		// The bridge treats this as non-fatal too.
		s.log.Warn().Err(err).Msg("failed to fetch Google Messages config")
	} else if s.sess.AuthData.IsGoogleAccount() && s.cli.Config.GetDeviceInfo().GetEmail() == "" {
		return fmt.Errorf("%w: Google account cookies no longer valid", events.ErrInvalidCredentials)
	}
	// Connect starts long polling in the background; the context must outlive
	// this call.
	return s.cli.Connect(ctx)
}

func (s *Service) shutdown() {
	s.mu.Lock()
	if s.cli != nil {
		s.cli.Disconnect()
	}
	s.mu.Unlock()
	if s.State() != StateLoggedOut {
		s.state.Store(StateStopped)
	}
	s.save()
}

func (s *Service) save() {
	data, err := s.store.SaveIfChanged(s.sess, s.lastSaved)
	if err != nil {
		s.log.Err(err).Msg("failed to save session")
		return
	}
	if !bytes.Equal(data, s.lastSaved) {
		s.log.Debug().Msg("session saved")
	}
	s.lastSaved = data
}

func isCredentialError(err error) bool {
	return errors.Is(err, events.ErrInvalidCredentials) || errors.Is(err, events.ErrRequestedEntityNotFound)
}

func (s *Service) signalLoggedOut() {
	select {
	case s.loggedOutCh <- struct{}{}:
	default:
	}
}

func (s *Service) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// handleEvent is called synchronously by libgm and must not block.
func (s *Service) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.AuthTokenRefreshed:
		s.signal(s.saveCh)
	case *events.ListenFatalError:
		if isCredentialError(evt.Error) || evt.Error.Error() == "http 401 while polling" {
			s.log.Error().Err(evt.Error).Msg("credentials rejected while polling")
			s.signalLoggedOut()
		} else {
			s.log.Error().Err(evt.Error).Msg("fatal listen error")
			s.signal(s.reconnectCh)
		}
	case *events.ListenTemporaryError:
		s.state.Store(StateReconnecting)
		s.log.Warn().Err(evt.Error).Msg("temporary listen error (libgm retries)")
	case *events.ListenRecovered:
		s.state.Store(StateConnected)
		s.log.Info().Msg("listen connection recovered")
	case *events.PhoneNotResponding:
		s.log.Warn().Msg("phone is not responding: keep it online with Google Messages allowed to run in background")
	case *events.PhoneRespondingAgain:
		s.log.Info().Msg("phone is responding again")
	case *events.PingFailed:
		if errors.Is(evt.Error, events.ErrRequestedEntityNotFound) {
			s.signalLoggedOut()
		} else {
			s.log.Warn().Err(evt.Error).Int("error_count", evt.ErrorCount).Msg("ping failed")
		}
	case *gmproto.RevokePairData:
		s.log.Warn().Msg("phone revoked the pairing")
		s.signalLoggedOut()
	case *events.GaiaLoggedOut:
		s.log.Warn().Msg("Google account logged out")
		s.signalLoggedOut()
	case *gmproto.Settings:
		s.simsMu.Lock()
		clear(s.sims)
		for _, sim := range evt.GetSIMCards() {
			s.sims[sim.GetSIMParticipant().GetID()] = sim
		}
		s.simsMu.Unlock()
	case *events.HackySetActiveMayFail:
		// The bridge re-sends SetActiveSession here; do the same off the
		// event goroutine.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			defer cancel()
			if cli := s.client(); cli != nil {
				if err := cli.SetActiveSession(ctx); err != nil {
					s.log.Warn().Err(err).Msg("set active session failed")
				}
			}
		}()
	default:
		// Incoming messages, typing notifications etc. are not needed: tools
		// fetch on demand.
		s.log.Trace().Type("event_type", rawEvt).Msg("ignored libgm event")
	}
}

func (s *Service) client() *libgm.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cli
}

func (s *Service) ready() (*libgm.Client, error) {
	switch s.State() {
	case StateLoggedOut:
		return nil, ErrLoggedOut
	case StateConnected:
		if cli := s.client(); cli != nil {
			return cli, nil
		}
	}
	return nil, ErrNotConnected
}

// Backoff returns the delay before reconnect attempt n (0-based): exponential
// from 5 s to 5 min with +-20 % jitter.
func Backoff(n int) time.Duration {
	d := backoffMin
	for i := 0; i < n && d < backoffMax; i++ {
		d *= 2
	}
	d = min(d, backoffMax)
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * jitter)
}

// ListConversations implements Client.
func (s *Service) ListConversations(ctx context.Context, limit int) ([]Conversation, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	var out []Conversation
	var cursor *gmproto.Cursor
	for len(out) < limit {
		req := &gmproto.ListConversationsRequest{
			Count:  int64(min(limit-len(out), 100)),
			Folder: gmproto.ListConversationsRequest_INBOX,
			Cursor: cursor,
		}
		resp, err := cli.ListConversations(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing conversations: %w", err)
		}
		for _, c := range resp.GetConversations() {
			out = append(out, convertConversation(c))
		}
		next := resp.GetCursor()
		if len(resp.GetConversations()) == 0 || next == nil || next.GetLastItemID() == "" ||
			(cursor != nil && next.GetLastItemID() == cursor.GetLastItemID()) {
			break
		}
		cursor = next
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetConversation implements Client.
func (s *Service) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	conv, err := s.getRawConversation(ctx, id)
	if err != nil {
		return nil, err
	}
	c := convertConversation(conv)
	return &c, nil
}

func (s *Service) getRawConversation(ctx context.Context, id string) (*gmproto.Conversation, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	conv, err := cli.GetConversation(ctx, id)
	if errors.Is(err, events.ErrRequestedEntityNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("getting conversation: %w", err)
	} else if conv.GetConversationID() == "" {
		return nil, ErrNotFound
	}
	return conv, nil
}

// ReadMessages implements Client.
func (s *Service) ReadMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	// Fetch the conversation for participant names; failure is not fatal.
	var participants map[string]*gmproto.Participant
	if conv, err := cli.GetConversation(ctx, conversationID); err == nil {
		participants = participantIndex(conv)
	}
	resp, err := cli.FetchMessages(ctx, conversationID, int64(limit), nil)
	if err != nil {
		return nil, fmt.Errorf("fetching messages: %w", err)
	}
	out := make([]Message, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		out = append(out, convertMessage(m, participants))
	}
	sortMessagesNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListContacts implements Client.
func (s *Service) ListContacts(ctx context.Context) ([]Contact, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	resp, err := cli.ListContacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing contacts: %w", err)
	}
	out := make([]Contact, 0, len(resp.GetContacts()))
	for _, c := range resp.GetContacts() {
		out = append(out, convertContact(c))
	}
	return out, nil
}

// GetOrCreateConversation implements Client. It mirrors the bridge's
// ResolveIdentifier(createChat=true) call.
func (s *Service) GetOrCreateConversation(ctx context.Context, number string) (*Conversation, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	resp, err := cli.GetOrCreateConversation(ctx, &gmproto.GetOrCreateConversationRequest{
		Numbers: []*gmproto.ContactNumber{{
			MysteriousInt: 2, // same value the bridge uses for 1:1 chats
			Number:        number,
			Number2:       number,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("creating conversation: %w", err)
	}
	if resp.GetConversation().GetConversationID() == "" {
		return nil, fmt.Errorf("no conversation returned (status %s)", resp.GetStatus())
	}
	c := convertConversation(resp.GetConversation())
	return &c, nil
}

// sendRetryBackoff matches the bridge: FAILURE_2/3 are transient.
var sendRetryBackoff = []time.Duration{3 * time.Second, 8 * time.Second}

// SendText implements Client. The request is built like the bridge's
// ConvertMatrixMessage for a plain text message.
func (s *Service) SendText(ctx context.Context, conversationID, text string) error {
	cli, err := s.ready()
	if err != nil {
		return err
	}
	conv, err := s.getRawConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if conv.GetReadOnly() {
		return errors.New("conversation is read-only")
	}
	sim := conv.GetSimCard()
	if sim.GetSIMData().GetSIMPayload() == nil {
		s.simsMu.RLock()
		if alt := s.sims[conv.GetDefaultOutgoingID()]; alt != nil {
			sim = alt
		}
		s.simsMu.RUnlock()
	}
	tmpID := util.GenerateTmpID()
	req := &gmproto.SendMessageRequest{
		ConversationID: conversationID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			ConversationID: conversationID,
			ParticipantID:  conv.GetDefaultOutgoingID(),
			TmpID2:         tmpID,
			MessageInfo: []*gmproto.MessageInfo{{
				Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}},
			}},
		},
		SIMPayload: sim.GetSIMData().GetSIMPayload(),
		TmpID:      tmpID,
	}
	resp, err := cli.SendMessage(ctx, req)
	for attempt := 0; err == nil && isTransientSendFailure(resp.GetStatus()) && attempt < len(sendRetryBackoff); attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sendRetryBackoff[attempt]):
		}
		resp, err = cli.SendMessage(ctx, req)
	}
	if errors.Is(err, libgm.ErrPhoneNotResponding) {
		return fmt.Errorf("phone did not respond; the message may still be sent when it comes back online: %w", err)
	} else if err != nil {
		return fmt.Errorf("sending message: %w", err)
	} else if resp.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		return fmt.Errorf("phone rejected the message (status %s)", resp.GetStatus())
	}
	return nil
}

func isTransientSendFailure(st gmproto.SendMessageResponse_Status) bool {
	return st == gmproto.SendMessageResponse_FAILURE_2 || st == gmproto.SendMessageResponse_FAILURE_3
}
