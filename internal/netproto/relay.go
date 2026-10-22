package netproto

const (
	MsgRelayRequest        MessageType = 193
	MsgRelayResponse       MessageType = 194
	CapabilityManagedRelay             = "managed-relay-v1"
)

// Refresh never accepts caller-supplied identity. Revoke targets a live session;
// AllSessions selects that session's canonical account after hierarchy checks.
type RelayRequest struct {
	Action      string `json:"action"`
	SessionID   string `json:"session_id,omitempty"`
	AllSessions bool   `json:"all_sessions,omitempty"`
}
type RelayResponse struct {
	Action     string      `json:"action"`
	SessionID  string      `json:"session_id"`
	Generation uint64      `json:"generation"`
	ServerTime int64       `json:"server_time"`
	IssuedAt   int64       `json:"issued_at,omitempty"`
	RefreshAt  int64       `json:"refresh_at,omitempty"`
	ExpiresAt  int64       `json:"expires_at,omitempty"`
	ICEServers []ICEServer `json:"ice_servers"`
}
