package store

import (
	"context"
	"fmt"
)

// ListComplaintPage returns up to limit+1 rows in native oldest-first order.
func (s *Store) ListComplaintPage(ctx context.Context, afterID int64, limit int) ([]Complaint, error) {
	if afterID < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid complaint page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, reporter, target, reason, created_at FROM complaints WHERE id > $1 ORDER BY id LIMIT $2`, afterID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("listing complaint page: %w", err)
	}
	defer closeRows(rows)
	result := make([]Complaint, 0, limit+1)
	for rows.Next() {
		var row Complaint
		if err := rows.Scan(&row.ID, &row.Reporter, &row.Target, &row.Reason, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
