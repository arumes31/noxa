package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"noxa/internal/e2ee/admission"
)

// ExpireOlmAttempts terminally fences only unsubmitted staging at its immutable
// deadline. Accepted delivery/history rows are outside this maintenance path.
func (s *Store) ExpireOlmAttempts(ctx context.Context) (int, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if e = admissionBudget(ctx, tx); e != nil {
		return 0, e
	}
	n, e := expireOlmAttempts(ctx, tx, 200, "")
	if e != nil {
		return 0, e
	}
	return n, tx.Commit()
}

func expireOlmAttempts(ctx context.Context, tx *sql.Tx, limit int, device string) (int, error) {
	rows, e := tx.QueryContext(ctx, `SELECT server,sender,device_id,account_epoch,receipt FROM olm_attempts WHERE state='staging' AND ($2='' OR device_id=$2) AND deadline<=EXTRACT(EPOCH FROM now())::bigint ORDER BY deadline,device_id LIMIT $1 FOR UPDATE`, limit, device)
	if e != nil {
		return 0, e
	}
	type item struct {
		source  OlmSource
		receipt admission.Receipt
	}
	items := []item{}
	for rows.Next() {
		var v item
		var b []byte
		if e = rows.Scan(&v.source.Server, &v.source.Principal, &v.source.Device, &v.source.AccountEpoch, &b); e != nil {
			rows.Close()
			return 0, e
		}
		if e = json.Unmarshal(b, &v.receipt); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, v := range items {
		v.receipt.State = "rejected"
		v.receipt.Reason = "deadline"
		if e = saveReceipt(ctx, tx, v.source, v.receipt); e != nil {
			return 0, e
		}
	}
	return len(items), nil
}
