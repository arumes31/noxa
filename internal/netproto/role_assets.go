package netproto

const (
	MsgRoleChannelIconSet   MessageType = 149
	MsgRoleChannelIconSaved MessageType = 150
)

// RoleChannelIconSaved confirms successful icon storage for this channel.
type RoleChannelIconSaved struct {
	ChannelID int64 `json:"channel_id"`
}
