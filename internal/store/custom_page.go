package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ListCustomPage returns at most limit+1 annotations in database key order.
// SQL caps each field before transferring it to Go: legacy unbounded values
// fail the page instead of being truncated or allocated in the integration.
func (s *Store) ListCustomPage(ctx context.Context, uniqueID, afterKey string, limit int) ([]CustomEntry, error) {
	if uniqueID == "" || len(uniqueID) > 128 || len(afterKey) > 128 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid custom metadata page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		CASE WHEN octet_length(key) <= 128 THEN key ELSE NULL END,
		CASE WHEN octet_length(value) <= 4096 THEN value ELSE NULL END
		FROM client_custom WHERE unique_id=$1 AND ($2='' OR key>$2)
		ORDER BY key LIMIT $3`, uniqueID, afterKey, limit+1)
	if err != nil {
		return nil, fmt.Errorf("querying custom metadata page: %w", err)
	}
	defer closeRows(rows)
	result := make([]CustomEntry, 0, limit+1)
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if !key.Valid || !value.Valid {
			return nil, fmt.Errorf("stored custom metadata exceeds supported size")
		}
		result = append(result, CustomEntry{Key: key.String, Value: value.String})
	}
	return result, rows.Err()
}
