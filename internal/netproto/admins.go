package netproto

// ServerAdminList requests the persistent admin roster. Only server admins
// may use it; regular permission/group grants do not authorize this request.
type ServerAdminList struct{}

// ServerAdminEntry identifies an administrator without exposing credentials.
type ServerAdminEntry struct {
	UniqueID string `json:"unique_id"`
	Nickname string `json:"nickname"`
}

// ServerAdmins includes offline identities as well as connected admins.
type ServerAdmins struct {
	Entries []ServerAdminEntry `json:"entries"`
}
