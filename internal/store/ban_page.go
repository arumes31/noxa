package store

import (
	"context"
	"fmt"
)

// ListBanPage returns at most limit+1 bans, newest first. The extra row lets
// the caller detect another page without counting or loading the whole table.
func (s *Store) ListBanPage(ctx context.Context, beforeID int64, limit int) ([]BanRecord, error) {
	if beforeID < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid ban page")
	}
	const q = `SELECT b.id, b.ban_type, b.value, COALESCE(b.reason, ''),
		COALESCE(u.unique_id, ''), b.banned_at, b.expires_at
		FROM bans b LEFT JOIN users u ON u.id = b.banned_by
		WHERE ($1::bigint = 0 OR b.id < $1)
		ORDER BY b.id DESC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, q, beforeID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("listing ban page: %w", err)
	}
	defer closeRows(rows)
	out := make([]BanRecord, 0, limit+1)
	for rows.Next() {
		var r BanRecord
		if err := rows.Scan(&r.ID, &r.Type, &r.Value, &r.Reason, &r.BannedBy, &r.CreatedAt, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
