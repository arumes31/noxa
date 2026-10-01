package netproto

const MsgChatAccepted MessageType = 154

const (
	ChatStored  = "stored"
	ChatRelayed = "relayed"
	ChatQueued  = "queued"
)

// ChatAccepted confirms the server accepted this exact destination/reference.
// Relayed means handed to the recipient's outgoing queue, not read or received.
// Queued means the offline spool write succeeded. Only Stored has a message ID.
type ChatAccepted struct {
	ClientMsgID string `json:"client_msg_id"`
	ChannelID   string `json:"channel_id,omitempty"`
	ToUniqueID  string `json:"to_unique_id,omitempty"`
	ToClientID  string `json:"to_client_id,omitempty"`
	Disposition string `json:"disposition"`
	MessageID   int64  `json:"message_id,omitempty"`
}

// Matches checks both destination correlation and the allowed outcome shape.
func (a ChatAccepted) Matches(msg ChatSend) bool {
	if msg.ClientMsgID == "" || a.ClientMsgID != msg.ClientMsgID || a.ChannelID != msg.ChannelID || a.ToUniqueID != msg.ToUniqueID || a.ToClientID != msg.ToClientID {
		return false
	}
	if msg.ToUniqueID == "" && msg.ToClientID == "" {
		return a.Disposition == ChatStored && a.MessageID > 0
	}
	return a.MessageID == 0 && (a.Disposition == ChatRelayed || (a.Disposition == ChatQueued && msg.ToUniqueID != ""))
}
