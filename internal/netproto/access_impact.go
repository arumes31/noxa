package netproto

import "noxa/internal/authorization"

const (
	MsgChannelAccessPreview MessageType = 151
	MsgChannelAccessImpact  MessageType = 152
)

// ChannelAccessPreview checks only this draft's channel and supplied subjects.
// UserID zero represents guests; at most 101 distinct IDs are accepted.
type ChannelAccessPreview struct {
	ScopeChannelID int64                            `json:"scope_channel_id,omitempty"`
	Change         authorization.RoleChange         `json:"change"`
	Tree           *authorization.ChannelTreeChange `json:"tree,omitempty"`
	UserIDs        []int64                          `json:"user_ids"`
}
