package netproto

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MsgPollRequest MessageType = 168
	MsgPollState   MessageType = 169
	PollPrefix                 = "[noxa-poll:v1]"
)

var ErrPollInvalid = errors.New("invalid poll")
var ErrPollClosed = errors.New("poll is closed")

// PollDefinition is carried inside the encrypted message body. Only option
// count, choice mode and expiry are stored separately for vote validation.
type PollDefinition struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Multiple bool     `json:"multiple"`
	ClosesAt int64    `json:"closes_at"`
}

func (p PollDefinition) Validate(now time.Time) error {
	if strings.TrimSpace(p.Question) == "" || utf8.RuneCountInString(p.Question) > 300 || len(p.Options) < 2 || len(p.Options) > 10 || p.ClosesAt <= now.Unix() || p.ClosesAt > now.Add(7*24*time.Hour).Unix() {
		return ErrPollInvalid
	}
	seen := map[string]bool{}
	for _, option := range p.Options {
		normalized := strings.ToLower(strings.TrimSpace(option))
		if normalized == "" || utf8.RuneCountInString(option) > 80 || seen[normalized] {
			return ErrPollInvalid
		}
		seen[normalized] = true
	}
	return nil
}

func EncodePoll(p PollDefinition) (string, error) {
	data, err := json.Marshal(p)
	return PollPrefix + string(data), err
}

func DecodePoll(body string) (PollDefinition, bool, error) {
	var p PollDefinition
	if !strings.HasPrefix(body, PollPrefix) {
		return p, false, nil
	}
	d := json.NewDecoder(strings.NewReader(strings.TrimPrefix(body, PollPrefix)))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, true, ErrPollInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, true, ErrPollInvalid
	}
	return p, true, nil
}

type PollRequest struct {
	MessageID int64  `json:"message_id"`
	Action    string `json:"action"` // get, vote, close
	Choices   []int  `json:"choices"`
}

// PollState reveals aggregate results and only the requesting user's ballot.
type PollState struct {
	Action      string `json:"action"`
	MessageID   int64  `json:"message_id"`
	Counts      []int  `json:"counts"`
	Choices     []int  `json:"choices"`
	TotalVoters int    `json:"total_voters"`
	Closed      bool   `json:"closed"`
	ClosesAt    int64  `json:"closes_at"`
	Version     uint64 `json:"version"`
}
