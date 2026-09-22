package broadcast

// IntegrationEvent is a current-policy projection, never a raw bus payload.
// A transport must serialize and finish bounded delivery inside its callback.
// Exactly one field is populated; raw bus sequence/time are deliberately absent.
type IntegrationEvent struct {
	Snapshot *TreeSnapshot
	Speaking *IntegrationSpeaking
}

type IntegrationSpeaking struct {
	ClientID  string
	ChannelID int64
	Speaking  bool
}
