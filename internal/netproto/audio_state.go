package netproto

const (
	MsgAudioStateSet     MessageType = 186
	MsgAudioStateSaved   MessageType = 187
	CapabilityAudioState             = "audio-state-v1"
)

// AudioStateSet advertises only the caller's local voice controls. It does not
// change moderator restrictions or authorize any media delivery.
type AudioStateSet struct {
	Muted    bool `json:"muted"`
	Deafened bool `json:"deafened"`
}

type AudioStateSaved struct {
	ClientID string `json:"client_id"`
	Muted    bool   `json:"muted"`
	Deafened bool   `json:"deafened"`
}
