package store

import (
	"context"
	"fmt"
)

// AdminIdentity identifies a server administrator without exposing credentials.
type AdminIdentity struct {
	UniqueID string
	Nickname string
}

// ListServerAdmins returns all server administrators, independently of group
// membership, ordered by nickname and unique ID.
func (s *Store) ListServerAdmins(ctx context.Context) ([]AdminIdentity, error) {
	const q = `SELECT unique_id, COALESCE(nickname, '')
		FROM users WHERE is_admin = TRUE ORDER BY nickname, unique_id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing server admins: %w", err)
	}
	defer closeRows(rows)
	var admins []AdminIdentity
	for rows.Next() {
		var admin AdminIdentity
		if err := rows.Scan(&admin.UniqueID, &admin.Nickname); err != nil {
			return nil, fmt.Errorf("scanning server admin: %w", err)
		}
		admins = append(admins, admin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating server admins: %w", err)
	}
	return admins, nil
}
