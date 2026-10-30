package netproto

const MsgStatusSaved MessageType = 161
const CapabilityLobbyPrivacy = "lobby_privacy_v1"

// StatusSaved confirms the caller's session presence was updated. Status is
// normalized (empty means online); Message is explicit even when cleared.
type StatusSaved struct {
	HideLobbyPresence *bool  `json:"hide_lobby_presence,omitempty"`
	ClientID          string `json:"client_id"`
	Status            string `json:"status"`
	Message           string `json:"message"`
}
