package netproto

const MsgMediaControlSaved MessageType = 162

// MediaControlSaved confirms a session control was applied, not persistence or
// media delivery. Every field is explicit, including empty lists and defaults.
type MediaControlSaved struct {
	Operation  MessageType `json:"operation"`
	ClientID   string      `json:"client_id"`
	Active     bool        `json:"active"`
	MaxHeight  int         `json:"max_height"`
	Quality    string      `json:"quality"`
	UniqueIDs  []string    `json:"unique_ids"`
	ChannelIDs []int64     `json:"channel_ids"`
}
