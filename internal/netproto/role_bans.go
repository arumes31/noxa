package netproto

const (
	MsgRoleBanRemove  MessageType = 147
	MsgRoleBanRemoved MessageType = 148
)

// RoleBanRemoved confirms that the selected ban no longer exists. Removing an
// already absent ID is an idempotent success; this does not lift other bans.
type RoleBanRemoved struct {
	BanID int64 `json:"ban_id"`
}
