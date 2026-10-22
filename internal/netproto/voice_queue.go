package netproto

const (
	MsgVoiceQueueRequest MessageType = 191
	MsgVoiceQueueResult  MessageType = 192
)

// ChannelID fences delayed clicks against membership changes. A hand grants
// no speaking permission and never changes microphone or moderator controls.
type VoiceQueueRequest struct {
	Action    string `json:"action"`
	ChannelID int64  `json:"channel_id"`
	ClientID  string `json:"client_id,omitempty"`
}
type VoiceQueueResult struct {
	ChannelID int64  `json:"channel_id"`
	Action    string `json:"action"`
	ClientID  string `json:"client_id,omitempty"`
}
