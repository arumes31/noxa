package netproto

import "encoding/base64"

const (
	MsgChannelInviteRequest MessageType = 189
	MsgChannelInviteResult  MessageType = 190
)

// Invites locate a channel; they never bypass its current permissions or password.
type ChannelInviteRequest struct {
	Action    string `json:"action"`
	ChannelID int64  `json:"channel_id"`
	Access    string `json:"access,omitempty"`
	Token     string `json:"token,omitempty"`
}

func ValidInviteToken(token string) bool {
	bytes, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(bytes) == 32 && len(token) == 43 && base64.RawURLEncoding.EncodeToString(bytes) == token
}
func (r ChannelInviteRequest) Valid() bool {
	if r.ChannelID <= 0 {
		return false
	}
	switch r.Action {
	case "get", "revoke":
		return r.Access == "" && r.Token == ""
	case "regenerate":
		return (r.Access == "guest" || r.Access == "member") && r.Token == ""
	case "resolve":
		return r.Access == "" && ValidInviteToken(r.Token)
	}
	return false
}

type ChannelInviteResult struct {
	Action    string `json:"action"`
	ChannelID int64  `json:"channel_id"`
	Access    string `json:"access,omitempty"`
	Active    bool   `json:"active"`
	Token     string `json:"token,omitempty"`
}
