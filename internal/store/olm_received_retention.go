package store

import (
	"context"

	"noxa/internal/e2ee/admission"
)

// ExpireReceivedOlm clears only ciphertext durably acknowledged by its exact
// recipient at least 30 days ago. Digest/ownership/ACK and logical replay fences
// remain; pending ciphertext and key obligations are outside this path (except
// stale pending controls, see ExpirePendingOlmControls).
func (s *Store) ExpireReceivedOlm(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH expired AS (
 SELECT id FROM olm_deliveries WHERE state='received' AND received_at<=now()-interval '30 days' AND octet_length(copy)>0
 ORDER BY received_at,id LIMIT 200 FOR UPDATE SKIP LOCKED
 ) UPDATE olm_deliveries d SET copy=''::bytea FROM expired e WHERE d.id=e.id`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ExpirePendingOlmControls clears pending media-control copies that were
// never acknowledged by admission.ControlPendingTTL after their manifest
// deadline (a control is useless by then: its expiry is at most
// MaxControlTTL after sending). A copy that opened a device-pair session
// (olm_sessions.initial_delivery) is kept: its initial-session obligation
// outlives the control. Cleared copies stop counting toward the per-sender
// bound and the target's queue. Bounded to 200 copies per pass.
func (s *Store) ExpirePendingOlmControls(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH expired AS (
 SELECT d.id FROM olm_deliveries d JOIN olm_messages o ON o.id=d.message_id
 WHERE d.state='pending' AND convert_from(o.manifest,'UTF8')::jsonb->>'purpose'='control'
 AND (convert_from(o.manifest,'UTF8')::jsonb->>'deadline')::bigint + $1 <= EXTRACT(EPOCH FROM now())::bigint
 AND NOT EXISTS (SELECT 1 FROM olm_sessions s WHERE s.initial_delivery=d.id)
 ORDER BY d.id LIMIT 200 FOR UPDATE OF d SKIP LOCKED
 ) UPDATE olm_deliveries d SET state='received', received_at=now(), copy=''::bytea FROM expired e WHERE d.id=e.id`, admission.ControlPendingTTL)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
