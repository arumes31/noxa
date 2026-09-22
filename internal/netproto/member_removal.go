package netproto

// MaxBanDurationSeconds fits the server's duration representation. Zero is a
// permanent ban; negative values are invalid.
const MaxBanDurationSeconds int64 = 9223372036

type MemberKick struct {
	ClientID string `json:"client_id"`
	Reason   string `json:"reason,omitempty"`
}

type MemberKickResult struct {
	ClientID       string `json:"client_id"`
	CleanupPending bool   `json:"cleanup_pending"`
}

type MemberBan struct {
	ClientID        string `json:"client_id"`
	Reason          string `json:"reason,omitempty"`
	DurationSeconds int64  `json:"duration_seconds"`
}

type BanPersistence string

const (
	BanSaved       BanPersistence = "saved"
	BanUnconfirmed BanPersistence = "unconfirmed"
)

// MemberBanResult acknowledges session revocation even when the database
// acknowledgement was lost. Only saved confirms persistence; callers must
// inspect the ban list before retrying an unconfirmed result.
type MemberBanResult struct {
	UniqueID       string         `json:"unique_id"`
	Persistence    BanPersistence `json:"persistence"`
	ExpiresAt      int64          `json:"expires_at"` // Unix milliseconds; meaningful only when saved
	CleanupPending bool           `json:"cleanup_pending"`
}
