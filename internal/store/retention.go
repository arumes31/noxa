package store

import (
	"context"
	"fmt"
	"time"
)

// PruneExpiredData removes a bounded batch from each retained data set. Pending
// deliveries are deliberately excluded. Repeated runs drain historical backlogs
// without a large startup transaction or synchronous work on the message path.
func (s *Store) PruneExpiredData(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '1s'; SET LOCAL statement_timeout = '3s'`); err != nil {
		return err
	}
	queries := []struct {
		sql    string
		cutoff time.Time
	}{
		{`DELETE FROM offline_messages WHERE id IN (
			SELECT id FROM offline_messages WHERE delivered_at < $1
			ORDER BY delivered_at, id LIMIT 500 FOR UPDATE SKIP LOCKED)`, now.UTC().Add(-30 * 24 * time.Hour)},
		{`DELETE FROM audit_log WHERE id IN (
			SELECT id FROM audit_log WHERE created_at < $1
			ORDER BY created_at, id LIMIT 500 FOR UPDATE SKIP LOCKED)`, now.UTC().Add(-180 * 24 * time.Hour)},
		{`DELETE FROM audit_log_daily WHERE (day, action, target) IN (
			SELECT day, action, target FROM audit_log_daily WHERE day < $1
			ORDER BY day, action, target LIMIT 500 FOR UPDATE SKIP LOCKED)`, now.UTC().Add(-180 * 24 * time.Hour).Truncate(24 * time.Hour)},
	}
	for _, query := range queries {
		if _, err = tx.ExecContext(ctx, query.sql, query.cutoff); err != nil {
			return fmt.Errorf("pruning retained data: %w", err)
		}
	}
	return tx.Commit()
}
