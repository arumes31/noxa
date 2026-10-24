package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrHistoryLimit reports a history copy over the size limit.
var ErrHistoryLimit = errors.New("history copy is too large")

// EndorseDevice records a trusted device's approval signature for a device
// that has none yet (a device linked with a passkey).
func (s *Store) EndorseDevice(ctx context.Context, principal, deviceID, approverID string, signature []byte) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `UPDATE user_devices SET approved_by=$3, approval_signature=$4
		WHERE principal=$1 AND device_id=$2 AND revoked_at IS NULL AND NOT first_device
			AND (approval_signature IS NULL OR length(approval_signature) = 0)
		RETURNING `+deviceColumns, principal, deviceID, approverID, signature))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("endorsing device: %w", err)
	}
	return d, nil
}

// HistoryChunk is one sealed chunk of a history copy.
type HistoryChunk struct {
	DeviceID     string
	Seq          int
	Total        int
	FromDeviceID string
	Data         []byte
	CreatedAt    time.Time
}

// PutHistoryChunk stores a chunk for an active device of the principal. A
// chunk 0 starts a new copy and replaces an earlier one. maxBytes bounds the
// copy's total size.
func (s *Store) PutHistoryChunk(ctx context.Context, principal string, c HistoryChunk, maxBytes int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storing history chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize writers of one device's copy so the size check holds.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('device-history:' || $1, 0))`, c.DeviceID); err != nil {
		return fmt.Errorf("storing history chunk: %w", err)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_devices WHERE device_id=$1 AND principal=$2 AND revoked_at IS NULL)`,
		c.DeviceID, principal).Scan(&active); err != nil {
		return fmt.Errorf("storing history chunk: %w", err)
	}
	if !active {
		return ErrDeviceNotFound
	}
	if c.Seq == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_history_chunks WHERE device_id=$1`, c.DeviceID); err != nil {
			return fmt.Errorf("storing history chunk: %w", err)
		}
	}
	var stored int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(data)), 0) FROM device_history_chunks WHERE device_id=$1 AND seq<>$2`, c.DeviceID, c.Seq).Scan(&stored); err != nil {
		return fmt.Errorf("storing history chunk: %w", err)
	}
	if stored+int64(len(c.Data)) > maxBytes {
		return ErrHistoryLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_history_chunks (device_id, seq, from_device_id, total, data)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id, seq) DO UPDATE SET from_device_id=EXCLUDED.from_device_id, total=EXCLUDED.total,
			data=EXCLUDED.data, created_at=now()`, c.DeviceID, c.Seq, c.FromDeviceID, c.Total, c.Data); err != nil {
		return fmt.Errorf("storing history chunk: %w", err)
	}
	return tx.Commit()
}

// HistoryChunks returns up to limit chunks for the device from seq onwards.
func (s *Store) HistoryChunks(ctx context.Context, principal, deviceID string, from, limit int) ([]HistoryChunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.device_id, c.seq, c.total, c.from_device_id, c.data, c.created_at
		FROM device_history_chunks c JOIN user_devices d ON d.device_id=c.device_id
		WHERE c.device_id=$1 AND d.principal=$2 AND d.revoked_at IS NULL AND c.seq >= $3
		ORDER BY c.seq LIMIT $4`, deviceID, principal, from, limit)
	if err != nil {
		return nil, fmt.Errorf("loading history chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HistoryChunk
	for rows.Next() {
		var c HistoryChunk
		if err := rows.Scan(&c.DeviceID, &c.Seq, &c.Total, &c.FromDeviceID, &c.Data, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("loading history chunks: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AckHistoryChunks deletes the device's chunks up to and including seq.
func (s *Store) AckHistoryChunks(ctx context.Context, principal, deviceID string, through int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_history_chunks c USING user_devices d
		WHERE d.device_id=c.device_id AND c.device_id=$1 AND d.principal=$2 AND c.seq <= $3`, deviceID, principal, through); err != nil {
		return fmt.Errorf("acknowledging history chunks: %w", err)
	}
	return nil
}

// ExpireHistoryChunks deletes chunks older than before.
func (s *Store) ExpireHistoryChunks(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM device_history_chunks WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("expiring history chunks: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}
