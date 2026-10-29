package store

import (
	"context"
	"database/sql"
	"errors"
)

// RetireOlmReceipt must be sent only after the exact terminal receipt commits in
// the local durable store. A contiguous fence replaces acknowledged detail, while
// pending deliveries and logical acceptance tombstones remain independently held.
func (s *Store) RetireOlmReceipt(ctx context.Context, a OlmSource, id, digest string) (int64, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = admissionBudget(ctx, tx); e != nil {
		return 0, e
	}
	_, r, e := loadAttempt(ctx, tx, a, id)
	if e != nil && !errors.Is(e, ErrOlmRetired) {
		return 0, e
	}
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return 0, e
	}
	if r.State != "retired" {
		if r.State == "staging" || r.Digest != digest {
			return 0, ErrOlmConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE olm_attempts SET receipt_ack=TRUE WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=$4`, a.Server, a.Device, a.AccountEpoch, id); e != nil {
			return 0, e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO olm_attempt_fences(server,device_id,account_epoch) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, a.Server, a.Device, a.AccountEpoch); e != nil {
		return 0, e
	}
	var through int64
	if e = tx.QueryRowContext(ctx, `SELECT retired_through FROM olm_attempt_fences WHERE server=$1 AND device_id=$2 AND account_epoch=$3 FOR UPDATE`, a.Server, a.Device, a.AccountEpoch).Scan(&through); e != nil {
		return 0, e
	}
	for n := 0; n < 200; n++ {
		var ack bool
		var state string
		e = tx.QueryRowContext(ctx, `SELECT receipt_ack,state FROM olm_attempts WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=($4::bigint)::text`, a.Server, a.Device, a.AccountEpoch, through+1).Scan(&ack, &state)
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return 0, e
		}
		if !ack || state == "staging" {
			break
		}
		through++
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM olm_attempts WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id::bigint<=$4 AND receipt_ack=TRUE`, a.Server, a.Device, a.AccountEpoch, through); e != nil {
		return 0, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE olm_attempt_fences SET retired_through=$4 WHERE server=$1 AND device_id=$2 AND account_epoch=$3`, a.Server, a.Device, a.AccountEpoch, through); e != nil {
		return 0, e
	}
	return through, tx.Commit()
}
