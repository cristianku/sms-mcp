// Package gm wraps go.mau.fi/mautrix-gmessages/pkg/libgm behind a small
// interface so the MCP layer can be tested without a paired phone.
package gm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Direction of a message relative to the paired phone.
type Direction string

const (
	Incoming Direction = "incoming"
	Outgoing Direction = "outgoing"
	System   Direction = "system"
)

// Conversation is a Google Messages conversation (SMS, MMS group or RCS chat).
type Conversation struct {
	ID                string
	Name              string
	Numbers           []string // phone numbers of the other participants
	IsGroup           bool
	Unread            bool
	ReadOnly          bool
	LastMessageTime   time.Time
	LastMessageText   string // snippet as shown by Google Messages (third-party text)
	LastMessageFromMe bool
}

// Message is one message in a conversation.
type Message struct {
	ID        string
	Time      time.Time
	Direction Direction
	Sender    string // display name or number; "me" for outgoing
	Number    string // sender number when known
	Text      string // third-party text; media parts are described in brackets
	Status    string
}

// Contact is an entry from the phone's address book.
type Contact struct {
	Name   string
	Number string
}

// Client is everything the MCP layer needs from Google Messages.
type Client interface {
	// ListConversations returns up to limit inbox conversations, newest first.
	ListConversations(ctx context.Context, limit int) ([]Conversation, error)
	// GetConversation returns a single conversation or ErrNotFound.
	GetConversation(ctx context.Context, id string) (*Conversation, error)
	// ReadMessages returns up to limit messages, newest first.
	ReadMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)
	// ListContacts returns the phone's contacts.
	ListContacts(ctx context.Context) ([]Contact, error)
	// GetOrCreateConversation returns the 1:1 conversation with number,
	// creating it on the phone if it does not exist yet.
	GetOrCreateConversation(ctx context.Context, number string) (*Conversation, error)
	// SendText sends a text message to an existing conversation.
	SendText(ctx context.Context, conversationID, text string) error
}

var (
	// ErrNotFound is returned when a conversation does not exist.
	ErrNotFound = errors.New("conversation not found")
	// ErrNotConnected is returned while the phone session is not connected.
	ErrNotConnected = errors.New("not connected to Google Messages (phone offline, reconnecting, or not paired)")
	// ErrInvalidNumber is returned by NormalizeNumber.
	ErrInvalidNumber = errors.New("invalid phone number")
)

// NormalizeNumber strips spaces and common separators from a phone number.
// International numbers keep a leading "+" and must have 8-15 digits; numbers
// without "+" (short codes, national format) must have 3-15 digits.
func NormalizeNumber(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidNumber)
	}
	plus := strings.HasPrefix(s, "+")
	if plus {
		s = s[1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", fmt.Errorf("%w: unexpected character %q", ErrInvalidNumber, r)
		}
	}
	digits := b.String()
	minDigits := 3
	if plus {
		minDigits = 8
	}
	if len(digits) < minDigits || len(digits) > 15 {
		return "", fmt.Errorf("%w: need %d-15 digits", ErrInvalidNumber, minDigits)
	}
	if plus {
		return "+" + digits, nil
	}
	return digits, nil
}

// SameNumber reports whether two numbers are equal after normalisation.
// Matching is exact on purpose: it backs the "only existing conversations"
// safety check, where a fuzzy match could let a message through.
func SameNumber(a, b string) bool {
	na, errA := NormalizeNumber(a)
	nb, errB := NormalizeNumber(b)
	return errA == nil && errB == nil && na == nb
}
