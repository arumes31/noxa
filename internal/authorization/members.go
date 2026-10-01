package authorization

// MemberQuery searches registered identities for the authorized management
// scope. Pagination is by immutable user ID; role assignments share Revision.
type MemberQuery struct {
	ChannelID        int64  `json:"channel_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Search           string `json:"search"`
	AfterID          int64  `json:"after_id"`
}

type MemberIdentity struct {
	UserID     int64   `json:"user_id"`
	UniqueID   string  `json:"unique_id"`
	Nickname   string  `json:"nickname"`
	RoleIDs    []int64 `json:"role_ids"`
	Manageable bool    `json:"manageable"`
}

type MemberPage struct {
	Revision int64            `json:"revision"`
	Entries  []MemberIdentity `json:"entries"`
	More     bool             `json:"more"`
}
