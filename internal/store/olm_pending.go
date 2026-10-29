package store

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Pending-ciphertext budgets (noxa-olm-admission-v1). Copies stay pending until
// the exact recipient device acknowledges them; nothing readable is evicted for
// capacity. Copies that can never be read again are closed instead (state
// received, payload emptied, digest kept): see closeEndedDeviceDeliveries and
// ExpireAbandonedOlmDeliveries. (Variables only so tests can scale them down.)
var (
	// OlmPendingGlobalBytes bounds all pending copies on the server.
	OlmPendingGlobalBytes int64 = 1 << 30
	// OlmPendingAccountBytes bounds pending copies held for one recipient
	// account (all of its devices); OlmPendingSenderBytes bounds one sender
	// account's outstanding chat copies. Neither applies to controls.
	OlmPendingAccountBytes int64 = 128 << 20
	OlmPendingSenderBytes  int64 = 128 << 20
	// OlmPendingDeviceBytes and OlmPendingDeviceCopies bound one device queue.
	OlmPendingDeviceBytes  int64 = 64 << 20
	OlmPendingDeviceCopies       = 4096
)

const (
	// Chat stops this far below each limit; only controls (media rekeys,
	// themselves bounded per sender/target pair and expired by maintenance)
	// may use the reserve, so a chat backlog never blocks key distribution.
	olmPendingControlReserve      = 1.0 / 16
	olmPendingDeviceControlCopies = 256
	// OlmAbandonedEpochGrace is how long copies to a replaced (reset) account
	// epoch stay drainable before maintenance closes them.
	OlmAbandonedEpochGrace = 7 * 24 * time.Hour
	// olmPendingWarnFraction triggers the near-capacity warning.
	olmPendingWarnFraction = 0.8
)

// pendingLimit returns the byte limit for a chat copy (limit minus the control
// reserve) or a control (the full limit).
func pendingLimit(limit int64, control bool) int64 {
	if control {
		return limit
	}
	return limit - int64(float64(limit)*olmPendingControlReserve)
}

// closeEndedDeviceDeliveries closes the pending copies of devices in the
// `revoked` CTE (endingDevices): an ended device can never acknowledge, and
// its history chunks are deleted in the same statement, so nothing readable is
// lost. The digest stays as the tombstone.
const closeEndedDeviceDeliveries = `UPDATE olm_deliveries d SET state='received', received_at=now(), copy=''::bytea
		FROM revoked WHERE d.device_id = revoked.device_id AND d.state='pending'`

// ExpireAbandonedOlmDeliveries closes, in bounded batches, pending copies that
// can no longer be read: their device is revoked or gone, or their account
// epoch was replaced by a reset more than OlmAbandonedEpochGrace ago. It
// returns the number of copies closed.
func (s *Store) ExpireAbandonedOlmDeliveries(ctx context.Context) (int64, error) {
	r, e := s.db.ExecContext(ctx, `WITH abandoned AS (
		SELECT d.id FROM olm_deliveries d LEFT JOIN user_devices u ON u.device_id=d.device_id
		WHERE d.state='pending' AND (u.device_id IS NULL OR u.revoked_at IS NOT NULL OR EXISTS (
			SELECT 1 FROM olm_account_history h WHERE h.device_id=d.device_id AND h.account_epoch=d.account_epoch
			AND h.retired_at <= now()-make_interval(secs => $1)))
		ORDER BY d.id LIMIT 1000 FOR UPDATE OF d SKIP LOCKED
	) UPDATE olm_deliveries d SET state='received', received_at=now(), copy=''::bytea FROM abandoned a WHERE d.id=a.id`, OlmAbandonedEpochGrace.Seconds())
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}

// OlmPendingUsage returns the bytes of all pending copies and the global limit.
func (s *Store) OlmPendingUsage(ctx context.Context) (int64, int64, error) {
	var used int64
	e := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(octet_length(copy)),0) FROM olm_deliveries WHERE state='pending'`).Scan(&used)
	return used, OlmPendingGlobalBytes, e
}

// notePendingUsage warns (at most once a minute) when the global pending
// budget is nearly used.
func (s *Store) notePendingUsage(global int64) {
	if float64(global) >= float64(OlmPendingGlobalBytes)*olmPendingWarnFraction {
		s.warnPending("Olm pending ciphertext budget nearly exhausted", global, "")
	}
}

// notePendingRefusal warns (rate-limited) when a limit refused a copy.
func (s *Store) notePendingRefusal(limit string, global int64) {
	s.warnPending("Olm pending ciphertext limit refused an admission", global, limit)
}

func (s *Store) warnPending(msg string, global int64, limit string) {
	if s.logger == nil {
		return
	}
	now := time.Now().Unix()
	last := s.pendingWarnedAt.Load()
	if now-last < 60 || !s.pendingWarnedAt.CompareAndSwap(last, now) {
		return
	}
	fields := []zap.Field{zap.Int64("pending_bytes", global), zap.Int64("limit_bytes", OlmPendingGlobalBytes)}
	if limit != "" {
		fields = append(fields, zap.String("limit", limit))
	}
	s.logger.Warn(msg, fields...)
}
