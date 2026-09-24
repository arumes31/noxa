package netproto

import "errors"

const (
	MsgCallRequest MessageType = 172
	MsgCallResult  MessageType = 173
)

var ErrCallDenied = errors.New("call unavailable or access denied")
var ErrCallInvalid = errors.New("invalid call action")

type CallParticipant struct {
	UniqueID string `json:"unique_id"`
	ClientID string `json:"client_id"`
	State    string `json:"state"` // ringing, accepted, declined, missed, left
}

type CallSession struct {
	ID             string            `json:"id"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Caller         string            `json:"caller"`
	CreatedAt      int64             `json:"created_at"`
	RingUntil      int64             `json:"ring_until"`
	EndedAt        int64             `json:"ended_at,omitempty"`
	Revision       int64             `json:"revision"`
	Participants   []CallParticipant `json:"participants"`
}

type CallRequest struct {
	Action         string `json:"action"`
	ID             string `json:"id"`
	Target         string `json:"target,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Signal         string `json:"signal,omitempty"`
}

type CallResult struct {
	Action  string        `json:"action"`
	Call    *CallSession  `json:"call,omitempty"`
	History []CallSession `json:"history,omitempty"`
}

type CallSignal struct {
	CallID string `json:"call_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Body   string `json:"body"`
}

func (c CallSession) Participant(uid string) (CallParticipant, bool) {
	for _, participant := range c.Participants {
		if participant.UniqueID == uid {
			return participant, true
		}
	}
	return CallParticipant{}, false
}

func (c CallSession) Change(actor, action string, now int64) (CallSession, error) {
	participant, found := c.Participant(actor)
	if !found {
		return CallSession{}, ErrCallDenied
	}
	if c.EndedAt != 0 {
		return CallSession{}, ErrCallInvalid
	}
	state := ""
	switch action {
	case "accept":
		if participant.State != "ringing" || now >= c.RingUntil {
			return CallSession{}, ErrCallInvalid
		}
		state = "accepted"
	case "decline":
		if participant.State != "ringing" {
			return CallSession{}, ErrCallInvalid
		}
		state = "declined"
	case "leave":
		if participant.State != "accepted" {
			return CallSession{}, ErrCallInvalid
		}
		state = "left"
	case "cancel":
		if actor != c.Caller || participant.State != "accepted" {
			return CallSession{}, ErrCallDenied
		}
		for _, peer := range c.Participants {
			if peer.UniqueID != actor && peer.State == "accepted" {
				return CallSession{}, ErrCallInvalid
			}
		}
		state = "left"
	default:
		return CallSession{}, ErrCallInvalid
	}
	next := c
	next.Participants = append([]CallParticipant(nil), c.Participants...)
	for i := range next.Participants {
		if next.Participants[i].UniqueID == actor {
			next.Participants[i].State = state
		}
	}
	next.Revision++
	next.finishIfEmpty(now)
	return next, nil
}

func (c *CallSession) finishIfEmpty(now int64) {
	accepted, ringing := 0, 0
	callerPresent := false
	for _, participant := range c.Participants {
		if participant.State == "accepted" {
			accepted++
			if participant.UniqueID == c.Caller {
				callerPresent = true
			}
		}
		if participant.State == "ringing" {
			ringing++
		}
	}
	if accepted >= 2 || (accepted == 1 && callerPresent && ringing > 0) {
		return
	}
	c.EndedAt = now
	for i := range c.Participants {
		if c.Participants[i].State == "ringing" {
			c.Participants[i].State = "missed"
		}
	}
}

func (c CallSession) Expire(now int64) (CallSession, bool) {
	if c.EndedAt != 0 || now < c.RingUntil {
		return c, false
	}
	next := c
	next.Participants = append([]CallParticipant(nil), c.Participants...)
	changed := false
	for i := range next.Participants {
		if next.Participants[i].State == "ringing" {
			next.Participants[i].State = "missed"
			changed = true
		}
	}
	if changed {
		next.Revision++
		next.finishIfEmpty(now)
	}
	return next, changed
}
