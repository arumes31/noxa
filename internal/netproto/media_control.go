package netproto

const MsgMediaControlSaved MessageType = 162

// CapabilityStreamVideoQuality advertises independently scoped receive layers.
const CapabilityStreamVideoQuality = "stream_video_quality_v1"

// MediaControlSaved confirms a session control was applied, not persistence or
// media delivery. Every field is explicit, including empty lists and defaults.
type MediaControlSaved struct {
	Operation   MessageType `json:"operation"`
	ClientID    string      `json:"client_id"`
	Active      bool        `json:"active"`
	MaxHeight   int         `json:"max_height"`
	Quality     string      `json:"quality"`
	UniqueIDs   []string    `json:"unique_ids"`
	ChannelIDs  []int64     `json:"channel_ids"`
	PublisherID string      `json:"publisher_id,omitempty"`
	Slot        string      `json:"slot,omitempty"`
	Generation  uint64      `json:"generation,string,omitempty"`
	Session     uint64      `json:"session,string,omitempty"`
}
