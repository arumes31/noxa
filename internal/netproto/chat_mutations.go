package netproto

const MsgChatMutationSaved MessageType = 153

// ChatMutationSaved confirms the requested mutation reached storage. It does
// not confirm delivery to every observer. A lost reply leaves the outcome
// unknown; clients must refresh rather than automatically retry a toggle.
type ChatMutationSaved struct {
	Operation MessageType `json:"operation"`
	MessageID int64       `json:"message_id"`
}
