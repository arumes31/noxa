package netproto

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MsgConversationRequest MessageType = 170
	MsgConversationResult  MessageType = 171
	MaxConversationMembers             = 16
)

var (
	ErrConversationInvalid  = errors.New("invalid conversation request")
	ErrConversationDenied   = errors.New("conversation access denied")
	ErrConversationConflict = errors.New("conversation changed; refresh and try again")
)

type ConversationMember struct {
	UniqueID    string `json:"unique_id"`
	Pending     bool   `json:"pending"`
	JoinedEpoch int64  `json:"joined_epoch"`
}

type Conversation struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Owner                string               `json:"owner"`
	Revision             int64                `json:"revision"`
	Epoch                int64                `json:"epoch"`
	Members              []ConversationMember `json:"members"`
	ReadMessageID        int64                `json:"read_message_id"`
	LatestMessageID      int64                `json:"latest_message_id"`
	UnreadCount          int                  `json:"unread_count"`
	ActiveCallCount      int                  `json:"active_call_count"`
	CallParticipantCount int                  `json:"call_participant_count"`
}

func (c Conversation) Member(uid string) (ConversationMember, bool) {
	for _, member := range c.Members {
		if member.UniqueID == uid {
			return member, true
		}
	}
	return ConversationMember{}, false
}

type ConversationRequest struct {
	Action        string            `json:"action"`
	ID            string            `json:"id"`
	Revision      int64             `json:"revision"`
	Name          string            `json:"name"`
	Target        string            `json:"target"`
	BeforeID      int64             `json:"before_id"`
	ReadMessageID int64             `json:"read_message_id"`
	Message       *ConversationSend `json:"message,omitempty"`
}

// Each current member receives a separately authenticated, encrypted envelope.
// There is no server-held group content key or plaintext message body.
type ConversationSend struct {
	Epoch     int64             `json:"epoch"`
	Reference string            `json:"reference"`
	Envelopes map[string]string `json:"envelopes"`
}

func (m ConversationSend) Validate(c Conversation, actor string) error {
	member, found := c.Member(actor)
	if !found || member.Pending {
		return ErrConversationDenied
	}
	if m.Epoch != c.Epoch {
		return ErrConversationConflict
	}
	if m.Reference == "" || len(m.Reference) > 128 || len(m.Envelopes) == 0 || len(m.Envelopes) > MaxConversationMembers {
		return ErrConversationInvalid
	}
	count := 0
	for _, recipient := range c.Members {
		if recipient.Pending {
			continue
		}
		count++
		body, found := m.Envelopes[recipient.UniqueID]
		if !found || len(body) > 24000 {
			return ErrConversationInvalid
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(body)
		if err != nil || len(raw) < 40 || len(raw) > 18000 || base64.StdEncoding.EncodeToString(raw) != body {
			return ErrConversationInvalid
		}
	}
	if count != len(m.Envelopes) {
		return ErrConversationInvalid
	}
	return nil
}

type ConversationMessage struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	Epoch          int64  `json:"epoch"`
	FromUniqueID   string `json:"from_unique_id"`
	Reference      string `json:"reference"`
	Body           string `json:"body"`
	CreatedAt      int64  `json:"created_at"`
}

type ConversationResult struct {
	Action        string                `json:"action"`
	Conversations []Conversation        `json:"conversations"`
	Messages      []ConversationMessage `json:"messages"`
	MessageID     int64                 `json:"message_id,omitempty"`
}

func ValidConversationName(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 80
}

// ChangeConversation applies only membership/metadata rules. Persistence must
// serialize it against sends and other changes using the same conversation row.
func ChangeConversation(current Conversation, actor string, request ConversationRequest) (Conversation, error) {
	member, exists := current.Member(actor)
	if !exists || actor == "" {
		return Conversation{}, ErrConversationDenied
	}
	if request.ID != current.ID || request.Revision != current.Revision {
		return Conversation{}, ErrConversationConflict
	}
	if member.Pending && request.Action != "accept" && request.Action != "decline" {
		return Conversation{}, ErrConversationDenied
	}
	next := current
	next.Members = append([]ConversationMember(nil), current.Members...)
	remove := func(uid string) {
		for i, m := range next.Members {
			if m.UniqueID == uid {
				next.Members = append(next.Members[:i], next.Members[i+1:]...)
				return
			}
		}
	}
	switch request.Action {
	case "invite":
		if actor != current.Owner {
			return Conversation{}, ErrConversationDenied
		}
		if request.Target == "" || len(request.Target) > 128 || !utf8.ValidString(request.Target) || len(current.Members) >= MaxConversationMembers {
			return Conversation{}, ErrConversationInvalid
		}
		if _, found := current.Member(request.Target); found {
			return Conversation{}, ErrConversationInvalid
		}
		next.Members = append(next.Members, ConversationMember{UniqueID: request.Target, Pending: true})
	case "accept":
		if !member.Pending {
			return Conversation{}, ErrConversationInvalid
		}
		next.Epoch++
		for i := range next.Members {
			if next.Members[i].UniqueID == actor {
				next.Members[i].Pending = false
				next.Members[i].JoinedEpoch = next.Epoch
			}
		}
	case "decline", "leave":
		if (request.Action == "decline") != member.Pending {
			return Conversation{}, ErrConversationInvalid
		}
		if actor == current.Owner && len(current.Members) > 1 {
			return Conversation{}, ErrConversationDenied
		}
		remove(actor)
		if !member.Pending {
			next.Epoch++
		}
	case "remove":
		if actor != current.Owner || request.Target == actor {
			return Conversation{}, ErrConversationDenied
		}
		target, found := current.Member(request.Target)
		if !found {
			return Conversation{}, ErrConversationInvalid
		}
		remove(request.Target)
		if !target.Pending {
			next.Epoch++
		}
	case "transfer":
		target, found := current.Member(request.Target)
		if actor != current.Owner || !found || target.Pending || request.Target == actor {
			return Conversation{}, ErrConversationDenied
		}
		next.Owner = request.Target
	case "rename":
		if actor != current.Owner {
			return Conversation{}, ErrConversationDenied
		}
		if !ValidConversationName(request.Name) {
			return Conversation{}, ErrConversationInvalid
		}
		next.Name = strings.TrimSpace(request.Name)
	default:
		return Conversation{}, ErrConversationInvalid
	}
	next.Revision++
	return next, nil
}
