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
// MaxControlTTL after sending). This includes a copy that opened a device-pair
// session (olm_sessions.initial_delivery): the server's obligation to deliver
// that opening message ends like for an acknowledgement, so its one-time key
// moves from admitted to received in the same statement (it stops counting
// toward the target's unresolved keys). The target device keeps the private
// key until its own closure, so later pre-key messages of that session still
// open. Cleared copies stop counting toward the per-sender bound and the
// target's queue. Bounded to 200 copies per pass.
func (s *Store) ExpirePendingOlmControls(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH expired AS (
 SELECT d.id FROM olm_deliveries d JOIN olm_messages o ON o.id=d.message_id
 WHERE d.state='pending' AND d.control
 AND (convert_from(o.manifest,'UTF8')::jsonb->>'deadline')::bigint + $1 <= EXTRACT(EPOCH FROM now())::bigint
 ORDER BY d.id LIMIT 200 FOR UPDATE OF d SKIP LOCKED
 ), released AS (
 UPDATE olm_directory_keys k SET lifecycle='received' FROM olm_sessions s JOIN expired e ON s.initial_delivery=e.id
 WHERE k.device_id=s.target_device AND k.account_epoch=s.target_epoch AND k.request_id=s.claim_id
 AND k.claimed_device=s.source_device AND k.claimed_epoch=s.source_epoch AND k.lifecycle='admitted'
 ) UPDATE olm_deliveries d SET state='received', received_at=now(), copy=''::bytea FROM expired e WHERE d.id=e.id`, admission.ControlPendingTTL)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
