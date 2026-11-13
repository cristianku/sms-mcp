package gm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestNormalizeNumber(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"+1 555-010-0123", "+15550100123", false},
		{"(555) 010.0123", "5550100123", false},
		{"12345", "12345", false},
		{"+1555", "", true}, // too short for international
		{"12", "", true},    // too short
		{"+1555abc0100", "", true},
		{"", "", true},
		{"+1234567890123456", "", true}, // 16 digits
	}
	for _, c := range cases {
		got, err := NormalizeNumber(c.in)
		if (err != nil) != c.err {
			t.Errorf("NormalizeNumber(%q) err=%v, want err=%v", c.in, err, c.err)
			continue
		}
		if err != nil && !errors.Is(err, ErrInvalidNumber) {
			t.Errorf("error %v does not wrap ErrInvalidNumber", err)
		}
		if got != c.want {
			t.Errorf("NormalizeNumber(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestSameNumber(t *testing.T) {
	if !SameNumber("+1 555 010 0123", "+15550100123") {
		t.Error("formatting differences should match")
	}
	if SameNumber("+15550100123", "5550100123") {
		t.Error("international vs national must not match (exact matching is deliberate)")
	}
	if SameNumber("bogus", "bogus") {
		t.Error("invalid numbers must never match")
	}
}

func TestConvertConversation(t *testing.T) {
	c := &gmproto.Conversation{
		ConversationID:       "42",
		Name:                 "Test Contact",
		LastMessageTimestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).UnixMicro(),
		LatestMessage:        &gmproto.LatestMessage{DisplayContent: "hello", FromMe: 1},
		Unread:               true,
		Participants: []*gmproto.Participant{
			{IsMe: true, ID: &gmproto.SmallInfo{Number: "+15550100000", ParticipantID: "1"}},
			{ID: &gmproto.SmallInfo{Number: "+15550100123", ParticipantID: "7"}, FullName: "Test Contact"},
			{ID: &gmproto.SmallInfo{ParticipantID: "8"}, FormattedNumber: "+1 555-010-0124"},
		},
	}
	got := convertConversation(c)
	if got.ID != "42" || got.Name != "Test Contact" || !got.Unread || !got.LastMessageFromMe || got.LastMessageText != "hello" {
		t.Fatalf("unexpected conversion: %+v", got)
	}
	if len(got.Numbers) != 2 || got.Numbers[0] != "+15550100123" || got.Numbers[1] != "+1 555-010-0124" {
		t.Fatalf("numbers: %v", got.Numbers)
	}
	if !got.LastMessageTime.Equal(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("time: %v", got.LastMessageTime)
	}
}

func TestConvertMessage(t *testing.T) {
	sender := &gmproto.Participant{ID: &gmproto.SmallInfo{Number: "+15550100123", ParticipantID: "7"}, FullName: "Test Contact"}
	in := &gmproto.Message{
		MessageID:     "m1",
		Timestamp:     time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).UnixMicro(),
		ParticipantID: "7",
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo: []*gmproto.MessageInfo{
			{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "line one"}}},
			{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{MimeType: "image/jpeg", MediaName: "x.jpg"}}},
		},
	}
	got := convertMessage(in, map[string]*gmproto.Participant{"7": sender})
	if got.Direction != Incoming || got.Sender != "Test Contact" || got.Number != "+15550100123" {
		t.Fatalf("sender/direction: %+v", got)
	}
	if got.Text != "line one\n[media: image/jpeg x.jpg]" {
		t.Fatalf("text: %q", got.Text)
	}

	out := &gmproto.Message{MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_OUTGOING_DELIVERED}}
	if m := convertMessage(out, nil); m.Direction != Outgoing || m.Sender != "me" {
		t.Fatalf("outgoing: %+v", m)
	}
	sys := &gmproto.Message{MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_TOMBSTONE_RCS_GROUP_CREATED}}
	if m := convertMessage(sys, nil); m.Direction != System {
		t.Fatalf("system: %+v", m)
	}
}

func TestSortMessagesNewestFirst(t *testing.T) {
	base := time.Unix(1000, 0)
	msgs := []Message{{ID: "a", Time: base}, {ID: "c", Time: base.Add(2 * time.Second)}, {ID: "b", Time: base.Add(time.Second)}}
	sortMessagesNewestFirst(msgs)
	if msgs[0].ID != "c" || msgs[1].ID != "b" || msgs[2].ID != "a" {
		t.Fatalf("order: %v", msgs)
	}
}

func TestBackoff(t *testing.T) {
	for n := 0; n < 20; n++ {
		d := Backoff(n)
		if d < 4*time.Second || d > 6*time.Minute {
			t.Fatalf("Backoff(%d)=%v out of range", n, d)
		}
	}
	if Backoff(0) > 6*time.Second {
		t.Fatal("first backoff should be about 5s")
	}
	if Backoff(15) < 4*time.Minute {
		t.Fatal("backoff should cap near 5 min")
	}
}

func fakeSession() *Session {
	ad := libgm.NewAuthData()
	ad.Browser = &gmproto.Device{UserID: 1, SourceID: "browser-test"}
	ad.Mobile = &gmproto.Device{UserID: 1, SourceID: "phone-test"}
	ad.TachyonAuthToken = []byte("not-a-real-token")
	return &Session{PairedAt: time.Unix(1700000000, 0).UTC(), PhoneID: "phone-test", AuthData: ad}
}

func TestSessionStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := &SessionStore{Path: path}
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("missing file: %v", err)
	}
	sess := fakeSession()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PhoneID != "phone-test" || got.AuthData.Browser.GetSourceID() != "browser-test" ||
		string(got.AuthData.TachyonAuthToken) != "not-a-real-token" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.AuthData.RequestCrypto == nil || len(got.AuthData.RequestCrypto.AESKey) == 0 {
		t.Fatal("request crypto keys lost")
	}
	// Unchanged session is not rewritten.
	prev, _ := Marshal(got)
	again, err := store.SaveIfChanged(got, prev)
	if err != nil || string(again) != string(prev) {
		t.Fatal("SaveIfChanged should be a no-op")
	}
	got.AuthData.TachyonAuthToken = []byte("rotated")
	changed, err := store.SaveIfChanged(got, prev)
	if err != nil || string(changed) == string(prev) {
		t.Fatal("SaveIfChanged should write a changed session")
	}
}

func TestSessionStoreFixesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	var warned string
	store := &SessionStore{Path: path, Warn: func(m string) { warned = m }}
	if err := store.Save(fakeSession()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode not fixed: %o", st.Mode().Perm())
	}
	if warned == "" {
		t.Fatal("expected a warning")
	}
}

func TestSessionStoreRejectsUnpaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"auth_data":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SessionStore{Path: path}).Load(); err == nil {
		t.Fatal("expected error for unpaired session")
	}
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SessionStore{Path: path}).Load(); err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestLoadGoogleCookies(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, []byte(`{"SID":"a","HSID":"b","OSID":"c","SSID":"d","APISID":"e","SAPISID":"f","__Secure-1PSIDTS":"g","NID":"drop"}`), 0o600)
	c, err := LoadGoogleCookies(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c["NID"]; ok {
		t.Fatal("unexpected cookie kept")
	}
	if c["__Secure-1PSIDTS"] != "g" {
		t.Fatal("optional cookie dropped")
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"SID":"a"}`), 0o600)
	if _, err := LoadGoogleCookies(bad); err == nil {
		t.Fatal("expected missing-cookie error")
	}
}

func TestServiceNotConnected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := &SessionStore{Path: path}
	if err := store.Save(fakeSession()); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListConversations(t.Context(), 1); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
	svc.state.Store(StateLoggedOut)
	if err := svc.SendText(t.Context(), "1", "x"); !errors.Is(err, ErrLoggedOut) {
		t.Fatalf("want ErrLoggedOut, got %v", err)
	}
}

func TestHandleEventSignals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := &SessionStore{Path: path}
	if err := store.Save(fakeSession()); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	svc.handleEvent(&events.AuthTokenRefreshed{})
	select {
	case <-svc.saveCh:
	default:
		t.Fatal("AuthTokenRefreshed should request a save")
	}
	svc.handleEvent(&events.ListenFatalError{Error: errors.New("boom")})
	select {
	case <-svc.reconnectCh:
	default:
		t.Fatal("non-credential fatal error should request a reconnect")
	}
	svc.handleEvent(&events.ListenFatalError{Error: events.ErrInvalidCredentials})
	select {
	case <-svc.loggedOutCh:
	default:
		t.Fatal("invalid credentials should log out")
	}
	svc.handleEvent(&gmproto.RevokePairData{})
	select {
	case <-svc.loggedOutCh:
	default:
		t.Fatal("revoke should log out")
	}
	svc.handleEvent(&gmproto.Settings{SIMCards: []*gmproto.SIMCard{{SIMParticipant: &gmproto.SIMParticipant{ID: "5"}}}})
	if svc.sims["5"] == nil {
		t.Fatal("SIM cards should be stored")
	}
	// Repeated signals never block.
	for range 5 {
		svc.handleEvent(&events.AuthTokenRefreshed{})
	}
}
