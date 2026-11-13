package gm

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// convertConversation maps a libgm conversation to the domain type.
func convertConversation(c *gmproto.Conversation) Conversation {
	out := Conversation{
		ID:                c.GetConversationID(),
		Name:              c.GetName(),
		IsGroup:           c.GetIsGroupChat(),
		Unread:            c.GetUnread(),
		ReadOnly:          c.GetReadOnly(),
		LastMessageText:   c.GetLatestMessage().GetDisplayContent(),
		LastMessageFromMe: c.GetLatestMessage().GetFromMe() == 1,
	}
	if ts := c.GetLastMessageTimestamp(); ts > 0 {
		out.LastMessageTime = time.UnixMicro(ts).UTC()
	}
	for _, p := range c.GetParticipants() {
		if p.GetIsMe() {
			continue
		}
		num := p.GetID().GetNumber()
		if num == "" {
			num = p.GetFormattedNumber()
		}
		if num != "" && !slices.Contains(out.Numbers, num) {
			out.Numbers = append(out.Numbers, num)
		}
	}
	return out
}

// participantIndex maps participant IDs to participants of a conversation.
func participantIndex(c *gmproto.Conversation) map[string]*gmproto.Participant {
	idx := make(map[string]*gmproto.Participant)
	for _, p := range c.GetParticipants() {
		if id := p.GetID().GetParticipantID(); id != "" {
			idx[id] = p
		}
	}
	return idx
}

// messageDirection derives the direction from the message status: libgm
// status codes below 100 are outgoing, 100-199 incoming, 200+ are tombstones
// (system events such as "group created").
func messageDirection(m *gmproto.Message) Direction {
	st := m.GetMessageStatus().GetStatus()
	switch {
	case st >= 200:
		return System
	case st >= 100:
		return Incoming
	case st > 0:
		return Outgoing
	}
	if m.GetSenderParticipant().GetIsMe() {
		return Outgoing
	}
	return Incoming
}

// convertMessage maps a libgm message to the domain type. participants may be
// nil; it is used to resolve sender names.
func convertMessage(m *gmproto.Message, participants map[string]*gmproto.Participant) Message {
	out := Message{
		ID:        m.GetMessageID(),
		Direction: messageDirection(m),
		Status:    strings.ToLower(m.GetMessageStatus().GetStatus().String()),
	}
	if ts := m.GetTimestamp(); ts > 0 {
		out.Time = time.UnixMicro(ts).UTC()
	}
	sender := m.GetSenderParticipant()
	if sender == nil && participants != nil {
		sender = participants[m.GetParticipantID()]
	}
	switch {
	case out.Direction == Outgoing:
		out.Sender = "me"
	case sender != nil:
		out.Number = sender.GetID().GetNumber()
		if out.Number == "" {
			out.Number = sender.GetFormattedNumber()
		}
		out.Sender = sender.GetFullName()
		if out.Sender == "" {
			out.Sender = out.Number
		}
	}
	var parts []string
	if subj := m.GetSubject(); subj != "" {
		parts = append(parts, "Subject: "+subj)
	}
	for _, info := range m.GetMessageInfo() {
		switch d := info.GetData().(type) {
		case *gmproto.MessageInfo_MessageContent:
			if t := d.MessageContent.GetContent(); t != "" {
				parts = append(parts, t)
			}
		case *gmproto.MessageInfo_MediaContent:
			mc := d.MediaContent
			desc := mc.GetMimeType()
			if desc == "" {
				desc = "attachment"
			}
			if name := mc.GetMediaName(); name != "" {
				desc += " " + name
			}
			parts = append(parts, fmt.Sprintf("[media: %s]", desc))
		}
	}
	out.Text = strings.Join(parts, "\n")
	return out
}

// convertContact maps a libgm contact.
func convertContact(c *gmproto.Contact) Contact {
	name := c.GetName()
	if name == "" {
		name = c.GetNameAgain()
	}
	num := c.GetNumber().GetNumber()
	if num == "" {
		num = c.GetNumber().GetFormattedNumber()
	}
	return Contact{Name: name, Number: num}
}

// sortMessagesNewestFirst sorts by time, newest first, keeping order stable
// for equal timestamps.
func sortMessagesNewestFirst(msgs []Message) {
	slices.SortStableFunc(msgs, func(a, b Message) int { return b.Time.Compare(a.Time) })
}
