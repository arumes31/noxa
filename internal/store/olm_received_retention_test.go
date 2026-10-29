package store

import (
	"context"
	"errors"
	"noxa/internal/e2ee/admission"
	"testing"
	"time"
)

func receivedRetentionFixture(t *testing.T) (*Store, OlmSource, OlmSource, admission.Manifest, admission.Receipt, OlmDelivery) {
	t.Helper()
	s, a, b, m, parts := admissionFixture(t)
	if _, err := s.BeginOlmAttempt(t.Context(), a, m); err != nil {
		t.Fatal(err)
	}
	for i, part := range parts {
		if err := s.PutOlmPart(t.Context(), a, m.AttemptID, i, part); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
	if err != nil || receipt.State != "accepted" {
		t.Fatal(receipt, err)
	}
	var deliveries []OlmDelivery
	err = s.WithOlmDeliveries(t.Context(), b, "", func(page OlmDeliveryPage) error { deliveries = page.Deliveries; return nil })
	if err != nil || len(deliveries) != 1 {
		t.Fatal(deliveries, err)
	}
	if err = s.AckOlmDelivery(t.Context(), b, deliveries[0].ID, deliveries[0].Digest); err != nil {
		t.Fatal(err)
	}
	return s, a, b, m, receipt, deliveries[0]
}

func TestOlmReceivedRetentionPreservesPendingAndReplayFences(t *testing.T) {
	s, a, b, m, receipt, delivery := receivedRetentionFixture(t)
	ctx := t.Context()
	var first time.Time
	if err := s.db.QueryRowContext(context.Background(), `SELECT received_at FROM olm_deliveries WHERE id=$1`, delivery.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first.IsZero() {
		t.Fatal("missing first durable ACK timestamp")
	}
	if err := s.AckOlmDelivery(ctx, b, delivery.ID, delivery.Digest); err != nil {
		t.Fatal(err)
	}
	var again time.Time
	if err := s.db.QueryRowContext(context.Background(), `SELECT received_at FROM olm_deliveries WHERE id=$1`, delivery.ID).Scan(&again); err != nil || !first.Equal(again) {
		t.Fatal("duplicate ACK moved retention deadline", first, again, err)
	}
	// Preserve actual admitted bytes in a retained pending fixture and a recent ACK.
	for _, id := range []string{"retention-pending", "retention-recent"} {
		if _, err := s.db.ExecContext(context.Background(), `INSERT INTO olm_messages(id,server,sender,device_id,account_epoch,logical_id,manifest) SELECT $1,server,sender,device_id,account_epoch,$1,manifest FROM olm_messages WHERE id=$2`, id, receipt.MessageID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(context.Background(), `INSERT INTO olm_deliveries(id,message_id,principal,device_id,account_epoch,copy,digest,state,received_at) SELECT $1,$1,principal,device_id,account_epoch,copy,digest,CASE WHEN $1='retention-pending' THEN 'pending' ELSE 'received' END,CASE WHEN $1='retention-pending' THEN now()-interval '60 days' ELSE now()-interval '29 days' END FROM olm_deliveries WHERE id=$2`, id, delivery.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(context.Background(), `UPDATE olm_deliveries SET received_at=now()-interval '31 days' WHERE id=$1`, delivery.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireReceivedOlm(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := s.ExpireReceivedOlm(ctx); err != nil || n != 0 {
		t.Fatal("non-idempotent sweep", n, err)
	}
	var size int
	if err := s.db.QueryRowContext(context.Background(), `SELECT octet_length(copy) FROM olm_deliveries WHERE id=$1`, delivery.ID).Scan(&size); err != nil || size != 0 {
		t.Fatal(size, err)
	}
	for _, id := range []string{"retention-pending", "retention-recent"} {
		if err := s.db.QueryRowContext(context.Background(), `SELECT octet_length(copy) FROM olm_deliveries WHERE id=$1`, id).Scan(&size); err != nil || size == 0 {
			t.Fatal("protected ciphertext cleared", id, size, err)
		}
	}
	if err := s.AckOlmDelivery(ctx, b, delivery.ID, delivery.Digest); err != nil {
		t.Fatal("exact ACK tombstone lost", err)
	}
	if err := s.AckOlmDelivery(ctx, b, delivery.ID, "wrong"); !errors.Is(err, ErrOlmConflict) {
		t.Fatal("wrong digest accepted", err)
	}
	if err := s.AckOlmDelivery(ctx, a, delivery.ID, delivery.Digest); !errors.Is(err, ErrOlmAuthority) {
		t.Fatal("wrong recipient accepted", err)
	}
	var got []OlmDelivery
	if err := s.WithOlmDeliveries(ctx, b, "", func(page OlmDeliveryPage) error { got = page.Deliveries; return nil }); err != nil || len(got) != 1 || got[0].ID != "retention-pending" || got[0].Digest != delivery.Digest {
		t.Fatal("pending delivery unavailable", got, err)
	}
	if prior, err := s.FinalizeOlmAttempt(ctx, a, m.AttemptID); err != nil || prior != receipt {
		t.Fatal("acceptance rewritten", prior, err)
	}
	if _, err := s.RetireOlmReceipt(ctx, a, m.AttemptID, receipt.Digest); err != nil {
		t.Fatal(err)
	}
	if prior, err := s.BeginOlmAttempt(ctx, a, m); err != nil || prior.State != "retired" {
		t.Fatal("retired attempt resurrected", prior, err)
	}
	var logical int
	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM olm_messages WHERE server=$1 AND sender=$2 AND logical_id=$3`, a.Server, a.Principal, m.LogicalID).Scan(&logical); err != nil || logical != 1 {
		t.Fatal("logical replay fence lost", logical, err)
	}
}

func TestOlmReceivedRetentionSweepIsBounded(t *testing.T) {
	s, _, _, _, receipt, delivery := receivedRetentionFixture(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO olm_messages(id,server,sender,device_id,account_epoch,logical_id,manifest) SELECT 'retention-batch-'||n,server,sender,device_id,account_epoch,'retention-batch-'||n,manifest FROM olm_messages CROSS JOIN generate_series(1,200) n WHERE id=$1`, receipt.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO olm_deliveries(id,message_id,principal,device_id,account_epoch,copy,digest,state,received_at) SELECT 'retention-batch-'||n,'retention-batch-'||n,principal,device_id,account_epoch,copy,digest,'received',now()-interval '31 days' FROM olm_deliveries CROSS JOIN generate_series(1,200) n WHERE id=$1`, delivery.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), `UPDATE olm_deliveries SET received_at=now()-interval '31 days' WHERE id=$1`, delivery.ID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{200, 1, 0} {
		if n, err := s.ExpireReceivedOlm(ctx); err != nil || n != want {
			t.Fatal("sweep bound", n, want, err)
		}
	}
}
