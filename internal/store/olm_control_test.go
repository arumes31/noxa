package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
)

// controlSend runs begin/put/finalize for one control to target.
func controlSend(t *testing.T, s *Store, a OlmSource, snap OlmRosterSnapshot, target OlmSource, attempt, logical, claim string, deadline int64) (admission.Receipt, error) {
	t.Helper()
	var targets []admission.Target
	for _, tg := range snap.Targets {
		if tg.Local || tg.Device == target.Device {
			targets = append(targets, tg)
		}
	}
	m := admission.Manifest{Version: admission.Version, Server: a.Server, Sender: a.Principal, Device: a.Device, AccountEpoch: 1, AttemptID: attempt, LogicalID: logical,
		Purpose: "control", Scope: "direct", Peer: target.Principal, Deadline: deadline, Rosters: snap.Rosters, Targets: targets}
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: target.Principal, Device: target.Device, AccountEpoch: 1, SessionID: "session", ClaimID: claim, Envelope: fixtureEnvelope(m, target)}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginOlmAttempt(t.Context(), a, m); e != nil {
		return admission.Receipt{}, e
	}
	for i, p := range parts {
		if e = s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	return s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
}

// 1c-iii-b item 3: a control is admitted for exactly its one named, current
// target device (no fanout to the peer's other device), with a short
// deadline and a per-device pending bound; the same subset as chat is a
// roster rejection.
func TestOlmControlSingleTargetAdmission(t *testing.T) {
	s, a, b, chat, _ := admissionFixture(t)
	b2dev, _, b2bundle := olmFixture(t, s, "bob")
	// A linked (approved) second device of bob.
	if _, e := s.db.ExecContext(context.Background(), `UPDATE user_devices SET approved_by=$1, approval_signature='`+"\x01"+`' WHERE device_id=$2`, b.Device, b2dev.DeviceID); e != nil {
		t.Fatal(e)
	}
	if e := s.PublishOlmBundle(t.Context(), "bob", b2dev.DeviceID, b2bundle.Server, b2bundle); e != nil {
		t.Fatal(e)
	}
	b2 := OlmSource{Server: b.Server, Principal: "bob", Device: b2dev.DeviceID, Signing: b2dev.Ed25519Public, AccountEpoch: 1}
	snap, e := s.OlmSnapshot(t.Context(), a, "direct", "bob", "")
	if e != nil || len(snap.Targets) != 3 {
		t.Fatal(len(snap.Targets), e)
	}
	deadline := time.Now().Unix() + 120
	// Chat with only one of bob's devices: never admitted (complete fanout).
	chat.Rosters, chat.AttemptID, chat.Deadline = snap.Rosters, "1", deadline
	chat.Targets = nil
	for _, tg := range snap.Targets {
		if tg.Local || tg.Device == b.Device {
			chat.Targets = append(chat.Targets, tg)
		}
	}
	parts, e := admission.Freeze(&chat, []admission.Copy{{Principal: "bob", Device: b.Device, AccountEpoch: 1, SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(chat, b)}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginOlmAttempt(t.Context(), a, chat); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e = s.PutOlmPart(t.Context(), a, chat.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	if r, e := s.FinalizeOlmAttempt(t.Context(), a, chat.AttemptID); e != nil || r.State != "rejected" || r.Reason != "roster" {
		t.Fatalf("partial chat fanout: %+v %v", r, e)
	}
	// A control deadline beyond MaxControlTTL is refused at begin.
	if _, e = controlSend(t, s, a, snap, b, "2", "ctl-long", "claim", time.Now().Unix()+admission.MaxControlTTL+60); !errors.Is(e, admission.ErrInvalid) {
		t.Fatalf("long control deadline: %v", e)
	}
	// The control to exactly one device is accepted; only that device gets a copy.
	r, e := controlSend(t, s, a, snap, b, "3", "ctl-1", "claim", deadline)
	if e != nil || r.State != "accepted" {
		t.Fatalf("control: %+v %v", r, e)
	}
	count := func(dev OlmSource) int {
		var n int
		if e := s.WithOlmDeliveries(t.Context(), dev, "", func(p OlmDeliveryPage) error { n = len(p.Deliveries); return nil }); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count(b) != 1 || count(b2) != 0 {
		t.Fatalf("control fanout: %d %d", count(b), count(b2))
	}
	// A stale target (its directory state changed) is a roster rejection.
	stale := snap
	stale.Targets = append([]admission.Target(nil), snap.Targets...)
	for i := range stale.Targets {
		if stale.Targets[i].Device == b.Device {
			stale.Targets[i].Identity = "other"
		}
	}
	if r, e := controlSend(t, s, a, stale, b, "4", "ctl-stale", "", deadline); e != nil || r.State != "rejected" {
		t.Fatalf("stale control target: %+v %v", r, e)
	}
	// Pending controls per target device are bounded.
	attempt := 5
	for i := 1; i < admission.MaxPendingControls; i++ {
		r, e := controlSend(t, s, a, snap, b, strconv.Itoa(attempt), "ctl-n"+strconv.Itoa(i), "", deadline)
		attempt++
		if e != nil || r.State != "accepted" {
			t.Fatalf("control %d: %+v %v", i, r, e)
		}
	}
	if _, e := controlSend(t, s, a, snap, b, strconv.Itoa(attempt), "ctl-over", "", deadline); !errors.Is(e, ErrOlmLimit) {
		t.Fatalf("pending control bound: %v", e)
	}
	// Review b46 P3-3: the bound is per sender device. Another sender (bob's
	// second device, to his first) is not blocked by alice's pending controls.
	if _, e := s.ClaimOlmKey(t.Context(), "bob", b2.Device, "bob", b.Device, "claim-b2", deadline, b.Server); e != nil {
		t.Fatal(e)
	}
	b2.Signing = b2dev.Ed25519Public
	own, e := s.OlmSnapshot(t.Context(), b2, "direct", "bob", "")
	if e != nil {
		t.Fatal(e)
	}
	if r, e := controlSend(t, s, b2, own, b, "1", "ctl-b2", "claim-b2", deadline); e != nil || r.State != "accepted" {
		t.Fatalf("other sender blocked by alice's controls: %+v %v", r, e)
	}
}

// Review b7 P3-6: pending controls are cleared ControlPendingTTL after their
// deadline, except a copy that opened a device-pair session.
func TestOlmPendingControlsExpire(t *testing.T) {
	s, a, b, _, _ := admissionFixture(t)
	snap, e := s.OlmSnapshot(t.Context(), a, "direct", "bob", "")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Unix() + 120
	if r, e := controlSend(t, s, a, snap, b, "1", "ctl-initial", "claim", deadline); e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
	if r, e := controlSend(t, s, a, snap, b, "2", "ctl-plain", "", deadline); e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
	pending := func() int {
		var n int
		if e := s.WithOlmDeliveries(t.Context(), b, "", func(p OlmDeliveryPage) error { n = len(p.Deliveries); return nil }); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if n, e := s.ExpirePendingOlmControls(t.Context()); e != nil || n != 0 || pending() != 2 {
		t.Fatalf("fresh controls expired: %d %v (%d pending)", n, e, pending())
	}
	// Age both manifests past deadline + ControlPendingTTL.
	if _, e = s.db.ExecContext(context.Background(), `UPDATE olm_messages SET manifest=convert_to(jsonb_set(convert_from(manifest,'UTF8')::jsonb,'{deadline}',to_jsonb(($1)::bigint))::text,'UTF8') WHERE logical_id IN ('ctl-initial','ctl-plain')`,
		time.Now().Unix()-admission.ControlPendingTTL-1); e != nil {
		t.Fatal(e)
	}
	if n, e := s.ExpirePendingOlmControls(t.Context()); e != nil || n != 1 || pending() != 1 {
		t.Fatalf("stale control sweep: %d %v (%d pending)", n, e, pending())
	}
}
