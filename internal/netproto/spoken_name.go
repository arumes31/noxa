package netproto

const (
	MsgSpokenNameGet  MessageType = 207 // client -> server: spoken name clip of a present session
	MsgSpokenNameData MessageType = 208 // server -> client: the clip (24 kHz mono PCM16 WAV)
	// Advertised only while the server's speech engine passed its self-check.
	CapabilitySpokenNames = "server_spoken_names_v1"
)

// SpokenNameGet names a session, never free text, so the server only voices
// names of sessions currently present.
type SpokenNameGet struct {
	ClientID string `json:"client_id"`
	Language string `json:"language"` // en | de
}

// SpokenNameData answers SpokenNameGet. Key identifies the clip (language and
// normalized name), so clients can cache it across sessions with that name.
type SpokenNameData struct {
	ClientID   string `json:"client_id"`
	Language   string `json:"language"`
	Key        string `json:"key"`
	DataBase64 string `json:"data_base64"`
}
