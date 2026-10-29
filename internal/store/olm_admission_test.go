package store

import (
	"encoding/json"
	"errors"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/ratchet"
	"sync"
	"testing"
	"time"
)

func admissionFixture(t *testing.T) (*Store, OlmSource, OlmSource, admission.Manifest, [][]byte) {
	t.Helper()
	s := pollTestStore(t)
	a, _, ba := olmFixture(t, s, "alice")
	b, _, bb := olmFixture(t, s, "bob")
	if e := s.PublishOlmBundle(t.Context(), a.Principal, a.DeviceID, ba.Server, ba); e != nil {
		t.Fatal(e)
	}
	if e := s.PublishOlmBundle(t.Context(), b.Principal, b.DeviceID, bb.Server, bb); e != nil {
		t.Fatal(e)
	}
	sa := OlmSource{Server: ba.Server, Principal: a.Principal, Device: a.DeviceID, Signing: a.Ed25519Public, AccountEpoch: 1}
	sb := OlmSource{Server: bb.Server, Principal: b.Principal, Device: b.DeviceID, Signing: b.Ed25519Public, AccountEpoch: 1}
	snap, e := s.OlmSnapshot(t.Context(), sa, "direct", "bob", "")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Unix() + 300
	_, e = s.ClaimOlmKey(t.Context(), sa.Principal, sa.Device, sb.Principal, sb.Device, "claim", deadline, sa.Server)
	if e != nil {
		t.Fatal(e)
	}
	m := admission.Manifest{Version: admission.Version, Server: sa.Server, Sender: sa.Principal, Device: sa.Device, AccountEpoch: 1, AttemptID: "1", LogicalID: "logical", Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: deadline, Rosters: snap.Rosters, Targets: snap.Targets}
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: sb.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(m, sb)}})
	if e != nil {
		t.Fatal(e)
	}
	return s, sa, sb, m, parts
}
func TestOlmAdmissionAtomicRetryACK(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	r, e := s.BeginOlmAttempt(t.Context(), a, m)
	if e != nil || r.State != "staging" {
		t.Fatal(r, e)
	}
	if _, e = s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID); !errors.Is(e, ErrOlmIncomplete) {
		t.Fatal("partial accepted", e)
	}
	for i, p := range parts {
		if e = s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.PutOlmPart(t.Context(), a, m.AttemptID, 0, []byte("changed")); !errors.Is(e, ErrOlmConflict) {
		t.Fatal(e)
	}
	r, delivered, e := s.FinalizeOlmAttemptDelivered(t.Context(), a, m.AttemptID)
	if e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
	// The doorbell list: exactly the inserted copies, only on this transition.
	if len(delivered) != 1 || delivered[0].Principal != b.Principal || delivered[0].Device != b.Device {
		t.Fatalf("delivered %+v", delivered)
	}
	// A new device invalidates fresh fanout, but cannot mutate the committed receipt.
	olmFixture(t, s, "bob")
	same, again, e := s.FinalizeOlmAttemptDelivered(t.Context(), a, m.AttemptID)
	if e != nil || same != r || len(again) != 0 {
		t.Fatal(same, again, e)
	}
	var deliveries []OlmDelivery
	e = s.WithOlmDeliveries(t.Context(), b, "", func(ds OlmDeliveryPage) error { deliveries = ds.Deliveries; return nil })
	if e != nil || len(deliveries) != 1 {
		t.Fatal(len(deliveries), e)
	}
	d := deliveries[0]
	if e = s.AckOlmDelivery(t.Context(), a, d.ID, d.Digest); !errors.Is(e, ErrOlmAuthority) {
		t.Fatal("other device ack", e)
	}
	if e = s.AckOlmDelivery(t.Context(), b, d.ID, d.Digest); e != nil {
		t.Fatal(e)
	}
	if e = s.AckOlmDelivery(t.Context(), b, d.ID, d.Digest); e != nil {
		t.Fatal("retry", e)
	}
	var state string
	if e = s.db.QueryRow(`SELECT lifecycle FROM olm_directory_keys WHERE request_id='claim'`).Scan(&state); e != nil || state != "received" {
		t.Fatal(state, e)
	}
}
func TestOlmAdmissionAbortRaceAndImmutable(t *testing.T) {
	s, a, _, m, parts := admissionFixture(t)
	if _, e := s.BeginOlmAttempt(t.Context(), a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e := s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	results := make(chan admission.Receipt, 2)
	for _, abort := range []bool{true, false} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r admission.Receipt
			var e error
			if abort {
				r, e = s.AbortOlmAttempt(t.Context(), a, m)
			} else {
				r, e = s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
			}
			if e != nil {
				t.Error(e)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	var state string
	for r := range results {
		if state != "" && state != r.State {
			t.Fatal("race produced two outcomes")
		}
		state = r.State
	}
	m.LogicalID = "changed"
	if _, e := s.BeginOlmAttempt(t.Context(), a, m); !errors.Is(e, ErrOlmConflict) {
		t.Fatal("changed immutable attempt", e)
	}
}
func TestOlmClaimSurvivesPublicCleanup(t *testing.T) {
	s, a, _, m, parts := admissionFixture(t)
	if _, e := s.db.Exec(`UPDATE olm_directory_publications SET expires_at=now()-interval '2 days'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ExpireOlmDirectory(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.BeginOlmAttempt(t.Context(), a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e := s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	if r, e := s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID); e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
}

func fixtureEnvelope(m admission.Manifest, b OlmSource) json.RawMessage {
	e, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "session", Binding: admission.ExpectedBinding(m, admission.Target{Principal: b.Principal, Device: b.Device, AccountEpoch: b.AccountEpoch}), Message: json.RawMessage(`{"type":0,"body":"ciphertext"}`)})
	return e
}
func TestOlmAdmissionRejectsRoutingSubstitution(t *testing.T) {
	s, a, b, m, _ := admissionFixture(t)
	var env ratchet.Envelope
	json.Unmarshal(fixtureEnvelope(m, b), &env)
	env.Binding.MessageID = "different"
	raw, _ := json.Marshal(env)
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: b.Principal, Device: b.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: raw}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginOlmAttempt(t.Context(), a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e = s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	r, e := s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
	if e != nil || r.State != "rejected" {
		t.Fatal(r, e)
	}
}
