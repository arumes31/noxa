package store

// AuditDetail is a server-authored envelope. Explicit channel IDs let readers
// authorize the complete record without guessing scope from free-form text.
// A non-nil empty Channels slice explicitly marks a server-wide record.
type AuditDetail struct {
	Version  int     `json:"version"`
	Revision int64   `json:"revision,omitempty"`
	Channels []int64 `json:"channel_ids"`
	Text     string  `json:"text,omitempty"`
	Before   any     `json:"before,omitempty"`
	After    any     `json:"after,omitempty"`
}

const AuditDetailVersion = 1
