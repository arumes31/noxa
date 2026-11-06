package netproto

// Integration (bot) requests shared by gRPC and ServerQuery.

// MaxBulkDeleteMessages bounds one bulk message delete (838).
const MaxBulkDeleteMessages = 100

// MessageBulkDelete removes up to MaxBulkDeleteMessages messages of one
// channel (0 = server-wide chat) in one transaction.
type MessageBulkDelete struct {
	ChannelID  int64   `json:"channel_id"`
	MessageIDs []int64 `json:"message_ids"`
}

func (r MessageBulkDelete) Valid() bool {
	if r.ChannelID < 0 || len(r.MessageIDs) == 0 || len(r.MessageIDs) > MaxBulkDeleteMessages {
		return false
	}
	seen := make(map[int64]bool, len(r.MessageIDs))
	for _, id := range r.MessageIDs {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// MessageBulkDeleteResult lists what was deleted and what was not found
// (missing, already deleted or in another channel), each in request order.
type MessageBulkDeleteResult struct {
	Deleted  []int64 `json:"deleted"`
	NotFound []int64 `json:"not_found"`
}

// IntegrationScopesQuery names an integration account (807).
type IntegrationScopesQuery struct {
	UniqueID string `json:"unique_id"`
}

func (q IntegrationScopesQuery) Valid() bool {
	return q.UniqueID != "" && len(q.UniqueID) <= 128
}

// IntegrationScopesSet replaces an account's scope ceiling. Full clears the
// ceiling (legacy full access) and must not be combined with Scopes.
type IntegrationScopesSet struct {
	UniqueID string   `json:"unique_id"`
	Full     bool     `json:"full,omitempty"`
	Scopes   []string `json:"scopes"`
}

func (s IntegrationScopesSet) Valid() bool {
	return s.UniqueID != "" && len(s.UniqueID) <= 128 && (!s.Full || len(s.Scopes) == 0) && (s.Full || s.Scopes != nil)
}

// IntegrationScopes is an account's current ceiling.
type IntegrationScopes struct {
	UniqueID string   `json:"unique_id"`
	Full     bool     `json:"full"`
	Scopes   []string `json:"scopes"`
}
