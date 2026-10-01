package store

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID         int64
	Actor      string
	Action     string
	Target     string
	Detail     string
	CreatedAt  time.Time
	ChannelIDs []int64 // nil: unclassified history; empty: explicit server scope
}

// AuditScoped writes scope separately from potentially user-authored detail.
func (s *Store) AuditScoped(ctx context.Context, actor, action, target, detail string, channelIDs []int64) {
	const q = `INSERT INTO audit_log (actor_unique_id,action,target,detail,scope_channel_ids) VALUES($1,$2,$3,$4,$5)`
	_, _ = s.db.ExecContext(ctx, q, actor, action, target, detail, pq.Array(channelIDs))
}

// Audit writes an audit log entry.
func (s *Store) Audit(ctx context.Context, actor, action, target, detail string) {
	const q = `INSERT INTO audit_log (actor_unique_id, action, target, detail) VALUES ($1, $2, $3, $4)`
	if _, err := s.db.ExecContext(ctx, q, actor, action, target, detail); err != nil {
		// Audit is best-effort; it must never break the action itself.
		_ = err
	}
}

// AuditList returns up to limit audit entries, newest first, with id <
// beforeID (0 = latest page).
func (s *Store) AuditList(ctx context.Context, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, actor_unique_id, action, target, detail, created_at, scope_channel_ids FROM audit_log`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE id < $1 ORDER BY id DESC LIMIT $2`
		args = append(args, beforeID, limit)
	} else {
		q += ` ORDER BY id DESC LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing audit log: %w", err)
	}
	defer closeRows(rows)
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.CreatedAt, pq.Array(&e.ChannelIDs)); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
