package netproto

// Acknowledged moderation requires role authorization. Legacy unrequested
// MoveClient and KickClient operations retain their original wire behavior.
const (
	MsgClientMoved   MessageType = 156
	MsgClientRemoved MessageType = 157
)

// ClientMoved confirms the membership change for the submitted connection.
type ClientMoved struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
}

// ClientRemoved reports a completed membership change or session revocation.
// FromServer and Ban echo the request flags (Ban always removes from server).
// Persistence is present only for bans. Unconfirmed requires a ban-list read
// before retrying; the target's sessions have still been revoked.
type ClientRemoved struct {
	ClientID       string         `json:"client_id"`
	FromServer     bool           `json:"from_server"`
	Ban            bool           `json:"ban"`
	ChannelID      int64          `json:"channel_id,omitempty"` // prior channel for channel disconnects
	Persistence    BanPersistence `json:"persistence,omitempty"`
	CleanupPending bool           `json:"cleanup_pending"`
}

func (r ClientRemoved) Matches(msg KickClient) bool {
	if msg.ClientID == "" || r.ClientID != msg.ClientID || r.FromServer != msg.FromServer || r.Ban != msg.Ban {
		return false
	}
	if msg.Ban {
		return msg.ExpectedChannelID == 0 && r.ChannelID == 0 && (r.Persistence == BanSaved || r.Persistence == BanUnconfirmed)
	}
	if r.Persistence != "" {
		return false
	}
	if msg.FromServer {
		return msg.ExpectedChannelID == 0 && r.ChannelID == 0
	}
	return msg.ExpectedChannelID > 0 && r.ChannelID == msg.ExpectedChannelID && !r.CleanupPending
}
