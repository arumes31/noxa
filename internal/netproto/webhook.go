package netproto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MsgWebhookRequest MessageType = 182
	MsgWebhookResult  MessageType = 183
)

type WebhookRequest struct {
	Action    string `json:"action"`
	ChannelID int64  `json:"channel_id"`
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
}

func (r WebhookRequest) Valid() bool {
	if r.ChannelID <= 0 {
		return false
	}
	switch r.Action {
	case "list":
		return r.ID == 0
	case "revoke":
		return r.ID > 0
	case "create":
		if r.ID != 0 || strings.TrimSpace(r.Name) != r.Name || r.Name == "" || utf8.RuneCountInString(r.Name) > 60 {
			return false
		}
		for _, c := range r.Name {
			if unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
				return false
			}
		}
		return true
	}
	return false
}

type WebhookEntry struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channel_id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}
type WebhookResult struct {
	Action     string         `json:"action"`
	ChannelID  int64          `json:"channel_id"`
	Hooks      []WebhookEntry `json:"hooks"`
	ID         int64          `json:"id,omitempty"`
	Token      string         `json:"token,omitempty"`
	HealthPort int            `json:"health_port"`
}
