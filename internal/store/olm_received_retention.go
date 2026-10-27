package store

import "context"

// ExpireReceivedOlm clears only ciphertext durably acknowledged by its exact
// recipient at least 30 days ago. Digest/ownership/ACK and logical replay fences
// remain; pending ciphertext and key obligations are outside this path.
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
