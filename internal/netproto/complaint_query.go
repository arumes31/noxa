package netproto

// ComplaintQuery follows ascending IDs, matching the native oldest-first list.
type ComplaintQuery struct {
	AfterID int64 `json:"after_id,omitempty"`
	Limit   int   `json:"limit,omitempty"`
}

type ComplaintPageEntry struct {
	ID int64 `json:"id"`
	ComplaintEntry
}

type ComplaintPage struct {
	Entries     []ComplaintPageEntry `json:"entries"`
	NextAfterID int64                `json:"next_after_id,omitempty"`
}

// ComplaintClearResult acknowledges only the completed deletion. Refresh the
// list separately; zero deleted rows is an idempotent successful no-op.
type ComplaintClearResult struct {
	Deleted int64 `json:"deleted"`
}
