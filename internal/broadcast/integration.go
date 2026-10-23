package broadcast

// IntegrationEvent is a current-policy projection, never a raw bus payload.
// A transport must serialize and finish bounded delivery inside its callback.
// Exactly one field is populated; raw bus sequence/time are deliberately absent.
type IntegrationEvent struct {
	Snapshot *TreeSnapshot
	Speaking *IntegrationSpeaking
	Device   *IntegrationDevice
}

// IntegrationDevice is a linked-device change visible to administrators.
type IntegrationDevice struct {
	Action      string
	UniqueID    string
	Reason      string
	DeviceID    string
	Kind        string
	Name        string
	Fingerprint string
	FirstDevice bool
	ApprovedBy  string
	CreatedAt   int64
	LastSeenAt  int64
	RevokedAt   int64
}

type IntegrationSpeaking struct {
	ClientID  string
	ChannelID int64
	Speaking  bool
}
