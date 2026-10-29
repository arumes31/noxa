package store

import (
	"errors"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"testing"
	"time"
)

func TestOlmTerminalCompactionKeepsReplayFence(t *testing.T) {
	s, a, _, m, _ := admissionFixture(t)
	r, e := s.AbortOlmAttempt(t.Context(), a, m)
	if e != nil || r.State != "aborted" {
		t.Fatal(r, e)
	}
	if _, e = s.RetireOlmReceipt(t.Context(), a, m.AttemptID, "wrong"); !errors.Is(e, ErrOlmConflict) {
		t.Fatal(e)
	}
	through, e := s.RetireOlmReceipt(t.Context(), a, m.AttemptID, r.Digest)
	if e != nil || through != 1 {
		t.Fatal(through, e)
	}
	if r, e = s.BeginOlmAttempt(t.Context(), a, m); e != nil || r.State != "retired" {
		t.Fatal("resurrected", r, e)
	}
	if r, e = s.OlmAttemptStatus(t.Context(), a, "1"); e != nil || r.State != "retired" {
		t.Fatal(r, e)
	}
	bad := a
	bad.Signing = "wrong"
	if _, e = s.OlmAttemptStatus(t.Context(), bad, "999"); !errors.Is(e, ErrOlmAuthority) {
		t.Fatal("absent metadata bypassed session authority", e)
	}
	m.AttemptID = "2"
	m.LogicalID = "new"
	if r, e = s.AbortOlmAttempt(t.Context(), a, m); e != nil || r.State != "aborted" {
		t.Fatal(r, e)
	}
}
func TestOlmResetPreservesAcceptedInitialAndFencesOldSend(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	a, _, ba := olmFixture(t, s, "alice")
	b, k, bb := olmFixture(t, s, "bob")
	for _, v := range []olmdir.Bundle{ba, bb} {
		if e := s.PublishOlmBundle(ctx, v.Principal, v.DeviceID, v.Server, v); e != nil {
			t.Fatal(e)
		}
	}
	sa := OlmSource{ba.Server, a.Principal, a.DeviceID, a.Ed25519Public, 1, false}
	sb := OlmSource{bb.Server, b.Principal, b.DeviceID, b.Ed25519Public, 1, false}
	snap, e := s.OlmSnapshot(ctx, sa, "direct", "bob", "")
	if e != nil {
		t.Fatal(e)
	}
	until := time.Now().Unix() + 300
	claim, e := s.ClaimOlmKey(ctx, sa.Principal, sa.Device, sb.Principal, sb.Device, "initial", until, sa.Server)
	if e != nil {
		t.Fatal(e)
	}
	m := admission.Manifest{Version: admission.Version, Server: sa.Server, Sender: sa.Principal, Device: sa.Device, AccountEpoch: 1, AttemptID: "1", LogicalID: "message", Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: until, Rosters: snap.Rosters, Targets: snap.Targets}
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: sb.Principal, Device: sb.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "initial", Envelope: fixtureEnvelope(m, sb)}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginOlmAttempt(ctx, sa, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e = s.PutOlmPart(ctx, sa, "1", i, p); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.FinalizeOlmAttempt(ctx, sa, "1"); e != nil {
		t.Fatal(e)
	}
	_, _, fresh := olmFixture(t, s, "fresh-fixture")
	fresh.Principal = sb.Principal
	fresh.DeviceID = sb.Device
	fresh.AccountEpoch = 2
	fresh.Generation = 2
	signOlm(t, &fresh, k)
	digest, e := olmdir.PublicationDigest(fresh)
	if e != nil {
		t.Fatal(e)
	}
	reset, e := (olmdir.Reset{Version: olmdir.ResetVersion, Server: sb.Server, Principal: sb.Principal, Device: sb.Device, TransitionID: "reset-one", OldEpoch: 1, OldIdentity: bb.Identity, OldSigning: bb.Signing, NewEpoch: 2, NewIdentity: fresh.Identity, NewSigning: fresh.Signing, Generation: 2, PublicationDigest: digest, Reason: "lost"}).Sign(k)
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := s.ResetOlmAccount(ctx, sb, reset, fresh)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.ResetOlmAccount(ctx, sb, reset, fresh)
	if e != nil || again != receipt {
		t.Fatal(again, e)
	}
	reset.Reason = "restored"
	if _, e = s.ResetOlmAccount(ctx, sb, reset, fresh); !errors.Is(e, ErrOlmConflict) {
		t.Fatal("changed reset accepted", e)
	}
	if _, e = s.OlmSnapshot(ctx, sb, "direct", "alice", ""); !errors.Is(e, ErrOlmAccountChanged) {
		t.Fatal("old source epoch active", e)
	}
	if e = s.WithOlmDeliveries(ctx, sb, "", func(page OlmDeliveryPage) error {
		ds := page.Deliveries
		if len(ds) != 1 {
			t.Fatal("old pending lost", len(ds))
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	lifecycle, e := s.CloseOlmKeys(ctx, sb, []string{claim.KeyID})
	if e != nil || lifecycle[0].State != "admitted" {
		t.Fatal("reset retired accepted private key", lifecycle, e)
	}
}
