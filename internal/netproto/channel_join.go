package netproto

const MsgChannelJoined MessageType = 158

// ChannelJoined confirms the caller's committed membership change. ChannelID
// zero confirms leaving a channel, including an already-unassigned caller.
// It does not promise delivery of media, keys or membership events.
type ChannelJoined struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
}
