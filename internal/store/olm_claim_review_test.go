package store

import (
	"context"
	"noxa/internal/e2ee/admission"
	"strconv"
	"testing"
	"time"
)

func TestReviewOlmClaimIDScopedToRequester(t *testing.T) {
	s, alice, bob, template, aliceParts := admissionFixture(t)
	ctx := t.Context()
	d, _, bundle := olmFixture(t, s, "charlie")
	if err := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, bundle.Server, bundle); err != nil {
		t.Fatal(err)
	}
	source := OlmSource{Server: bundle.Server, Principal: d.Principal, Device: d.DeviceID, Signing: d.Ed25519Public, AccountEpoch: 1}
	if _, err := s.ClaimOlmKey(ctx, source.Principal, source.Device, bob.Principal, bob.Device, "claim", template.Deadline, source.Server, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.OlmSnapshot(ctx, source, "direct", bob.Principal, "")
	if err != nil {
		t.Fatal(err)
	}
	m := template
	m.Sender = source.Principal
	m.Device = source.Device
	m.LogicalID = "charlie-message"
	m.Rosters = snapshot.Rosters
	m.Targets = snapshot.Targets
	parts, err := admission.Freeze(&m, []admission.Copy{{Principal: bob.Principal, Device: bob.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(m, bob)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginOlmAttempt(ctx, source, m); err != nil {
		t.Fatal(err)
	}
	for i, part := range parts {
		if err = s.PutOlmPart(ctx, source, m.AttemptID, i, part); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := s.FinalizeOlmAttempt(ctx, source, m.AttemptID)
	if err != nil || receipt.State != "accepted" {
		t.Fatalf("valid independent requester reservation was not admitted: state=%q err=%v", receipt.State, err)
	}
	if _, err = s.BeginOlmAttempt(ctx, alice, template); err != nil {
		t.Fatal(err)
	}
	for i, p := range aliceParts {
		if err = s.PutOlmPart(ctx, alice, template.AttemptID, i, p); err != nil {
			t.Fatal(err)
		}
	}
	if r, e := s.FinalizeOlmAttempt(ctx, alice, template.AttemptID); e != nil || r.State != "accepted" {
		t.Fatal("original requester's claim", r, e)
	}

}

func TestOlmInlineExpiryReleasesPersistedParts(t *testing.T) {
	s, a, b, m, _ := admissionFixture(t)
	ctx := t.Context()
	for i := 1; i <= 4; i++ {
		m.AttemptID = strconv.Itoa(i)
		m.LogicalID = "expiry-" + m.AttemptID
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: b.Principal, Device: b.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(m, b)}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BeginOlmAttempt(ctx, a, m); e != nil {
			t.Fatal(e)
		}
		for n, p := range parts {
			if e = s.PutOlmPart(ctx, a, m.AttemptID, n, p); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e := s.db.ExecContext(context.Background(), `UPDATE olm_attempts SET deadline=0 WHERE device_id=$1`, a.Device); e != nil {
		t.Fatal(e)
	}
	m.AttemptID = "5"
	m.LogicalID = "fresh"
	if _, e := s.BeginOlmAttempt(ctx, a, m); e != nil {
		t.Fatal("expired local quota remains", e)
	}
	var count int
	if e := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM olm_parts WHERE device_id=$1`, a.Device).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired persisted parts", count, e)
	}
	for i := 1; i <= 4; i++ {
		r, e := s.OlmAttemptStatus(ctx, a, strconv.Itoa(i))
		if e != nil || r.State != "rejected" || r.Reason != "deadline" {
			t.Fatal(r, e)
		}
	}
}
func TestOlmSlowDeliveryDoesNotHoldGlobalAdmission(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	ctx := t.Context()
	d, _, bundle := olmFixture(t, s, "unrelated")
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, bundle.Server, bundle); e != nil {
		t.Fatal(e)
	}
	other := OlmSource{Server: bundle.Server, Principal: d.Principal, Device: d.DeviceID, Signing: d.Ed25519Public, AccountEpoch: 1}
	otherSnapshot, e := s.OlmSnapshot(ctx, other, "direct", b.Principal, "")
	if e != nil {
		t.Fatal(e)
	}
	otherM := m
	otherM.Sender, otherM.Device, otherM.LogicalID = other.Principal, other.Device, "unrelated"
	otherM.Rosters, otherM.Targets = otherSnapshot.Rosters, otherSnapshot.Targets
	if _, e := admission.Freeze(&otherM, []admission.Copy{{Principal: b.Principal, Device: b.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(otherM, b)}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.BeginOlmAttempt(ctx, a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e := s.PutOlmPart(ctx, a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.WithOlmDeliveries(ctx, b, "", func(OlmDeliveryPage) error { close(entered); <-release; return nil })
	}()
	defer func() {
		close(release)
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handoff never started")
	}
	op, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Task 4 review P3-1: the drain no longer holds the recipient's locks
	// across its write, so even an admission to that very recipient proceeds
	// at once while the write is stalled.
	started := time.Now()
	if r, e := s.FinalizeOlmAttempt(op, a, m.AttemptID); e != nil || r.State != "accepted" {
		t.Fatalf("admission to a recipient whose drain write is stalled: %+v %v", r, e)
	}
	if time.Since(started) > 750*time.Millisecond {
		t.Fatal("global admission blocked behind slow recipient")
	}
	if _, e := s.BeginOlmAttempt(op, other, otherM); e != nil {
		t.Fatal("unrelated admission blocked", e)
	}
}

func TestOlmInlineGlobalExpiryBoundPreservesAccepted(t *testing.T) {
	s, a, _, m, parts := admissionFixture(t)
	ctx := t.Context()
	if _, e := s.BeginOlmAttempt(ctx, a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e := s.PutOlmPart(ctx, a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.FinalizeOlmAttempt(ctx, a, m.AttemptID); e != nil {
		t.Fatal(e)
	}
	// Seed 201 abandoned attempts from other devices, below the global budget.
	// One begin must retire only its bounded 200-row global batch.
	if _, e := s.db.ExecContext(context.Background(), `INSERT INTO olm_attempts(server,sender,device_id,account_epoch,attempt_id,logical_id,manifest,digest,deadline,state,receipt)
	 SELECT server,sender,'expired-global',account_epoch,g::text,'expired-'||g,manifest,digest,0,'staging',
	 convert_to((convert_from(receipt,'UTF8')::jsonb || jsonb_build_object('attempt_id',g::text,'state','staging'))::text,'UTF8')
	 FROM olm_attempts CROSS JOIN generate_series(1,201) g WHERE device_id=$1`, a.Device); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(context.Background(), `INSERT INTO olm_parts(server,device_id,account_epoch,attempt_id,part_number,data)
	 SELECT server,device_id,account_epoch,attempt_id,0,'unsubmitted'::bytea FROM olm_attempts WHERE device_id='expired-global'`); e != nil {
		t.Fatal(e)
	}
	m.AttemptID, m.LogicalID = "2", "new-live"
	if _, e := s.BeginOlmAttempt(ctx, a, m); e != nil {
		t.Fatal(e)
	}
	var rejected, staging, remainingParts, pending int
	if e := s.db.QueryRowContext(context.Background(), `SELECT count(*) FILTER(WHERE state='rejected'),count(*) FILTER(WHERE state='staging') FROM olm_attempts WHERE device_id='expired-global'`).Scan(&rejected, &staging); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM olm_parts WHERE device_id='expired-global'`).Scan(&remainingParts); e != nil {
		t.Fatal(e)
	}
	if rejected != 200 || staging != 1 || remainingParts != 1 {
		t.Fatalf("expiry batch: rejected=%d staging=%d parts=%d", rejected, staging, remainingParts)
	}
	if r, e := s.OlmAttemptStatus(ctx, a, "1"); e != nil || r.State != "accepted" {
		t.Fatal("accepted receipt changed", r, e)
	}
	if e := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM olm_deliveries WHERE state='pending' AND octet_length(copy)>0`).Scan(&pending); e != nil || pending != 1 {
		t.Fatal("pending delivery changed", pending, e)
	}
}
