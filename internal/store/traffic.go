package store

import (
	"context"
	"database/sql"
	"errors"
	"noxa/internal/traffic"
	"time"
)

// Absolute per-instance checkpoints and GREATEST make ambiguous commit retries
// idempotent. One transaction every five minutes, never a packet-time DB write.
func (s *Store) SaveTrafficCheckpoint(ctx context.Context, instance string, started int64, total traffic.Counts, buckets []traffic.Bucket) error {
	if len(instance) != 32 || started <= 0 || total.In < 0 || total.Out < 0 || len(buckets) > 10000 {
		return errors.New("invalid traffic checkpoint")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO server_traffic_totals(instance_id,started_at,in_bytes,out_bytes) VALUES($1,$2,$3,$4)
 ON CONFLICT(instance_id) DO UPDATE SET in_bytes=GREATEST(server_traffic_totals.in_bytes,EXCLUDED.in_bytes),out_bytes=GREATEST(server_traffic_totals.out_bytes,EXCLUDED.out_bytes)`, instance, started, total.In, total.Out)
	if err != nil {
		return err
	}
	for _, bucket := range buckets {
		if bucket.Start <= 0 || bucket.Start%300 != 0 || bucket.In < 0 || bucket.Out < 0 {
			return errors.New("invalid traffic bucket")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO server_traffic_buckets(instance_id,bucket_start,in_bytes,out_bytes) VALUES($1,$2,$3,$4)
  ON CONFLICT(instance_id,bucket_start) DO UPDATE SET in_bytes=GREATEST(server_traffic_buckets.in_bytes,EXCLUDED.in_bytes),out_bytes=GREATEST(server_traffic_buckets.out_bytes,EXCLUDED.out_bytes)`, instance, bucket.Start, bucket.In, bucket.Out)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM server_traffic_buckets WHERE bucket_start < $1`, time.Now().Add(-31*24*time.Hour).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadTrafficHistory(ctx context.Context, now int64) (traffic.History, error) {
	result := traffic.History{Buckets: []traffic.Bucket{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(in_bytes),0),COALESCE(SUM(out_bytes),0),COALESCE(MIN(started_at),0) FROM server_traffic_totals`).Scan(&result.Total.In, &result.Total.Out, &result.Since)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT bucket_start,SUM(in_bytes),SUM(out_bytes) FROM server_traffic_buckets WHERE bucket_start >= $1 GROUP BY bucket_start ORDER BY bucket_start`, now-int64(31*24*time.Hour/time.Second))
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var bucket traffic.Bucket
		if err = rows.Scan(&bucket.Start, &bucket.In, &bucket.Out); err != nil {
			_ = rows.Close()
			return result, err
		}
		result.Buckets = append(result.Buckets, bucket)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
