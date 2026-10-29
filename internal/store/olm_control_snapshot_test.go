package store

import (
	"errors"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
)

func unpublishedOlmSibling(t *testing.T, s *Store, owner OlmSource) Device {
	t.Helper()
	d, _, _ := olmFixture(t, s, owner.Principal)
	if _, err := s.EndorseDevice(t.Context(), owner.Principal, d.DeviceID, owner.Device, []byte("approved")); err != nil {
		t.Fatal(err)
	}
	return d
}

func stageControlSnapshot(t *testing.T, s *Store, a, b OlmSource, snap OlmRosterSnapshot, attempt string) {
	t.Helper()
	m := admission.Manifest{Version: admission.Version, Server: a.Server, Sender: a.Principal, Device: a.Device, AccountEpoch: a.AccountEpoch,
		AttemptID: attempt, LogicalID: "targeted-" + attempt, Purpose: "control", Scope: "direct", Peer: b.Principal,
		Deadline: time.Now().Unix() + 120, Rosters: snap.Rosters, Targets: snap.Targets}
	parts, err := admission.Freeze(&m, []admission.Copy{{Principal: b.Principal, Device: b.Device, AccountEpoch: b.AccountEpoch,
		SessionID: "session", ClaimID: "claim", Envelope: fixtureEnvelope(m, b)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginOlmAttempt(t.Context(), a, m); err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if err = s.PutOlmPart(t.Context(), a, attempt, i, p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOlmControlSnapshotIgnoresUnpublishedSiblings(t *testing.T) {
	s, a, b, chat, parts := admissionFixture(t)
	unpublishedOlmSibling(t, s, a)
	unpublishedOlmSibling(t, s, b)
	if _, err := s.OlmSnapshot(t.Context(), a, "direct", b.Principal, ""); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("chat snapshot must still require every approved device: %v", err)
	}
	// A sender cannot use an earlier/partial snapshot to bypass full chat fanout.
	if _, err := s.BeginOlmAttempt(t.Context(), a, chat); err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if err := s.PutOlmPart(t.Context(), a, chat.AttemptID, i, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinalizeOlmAttempt(t.Context(), a, chat.AttemptID); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("partial chat finalize must fail: %v", err)
	}
	snap, err := s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device)
	if err != nil || len(snap.Targets) != 2 || len(snap.Rosters) != 2 {
		t.Fatalf("targeted snapshot: %+v %v", snap, err)
	}
	stageControlSnapshot(t, s, a, b, snap, "2")
	if r, err := s.FinalizeOlmAttempt(t.Context(), a, "2"); err != nil || r.State != "accepted" {
		t.Fatalf("targeted control: %+v %v", r, err)
	}
}

func TestOlmControlSnapshotSiblingChangeKeepsRosterFence(t *testing.T) {
	s, a, b, _, _ := admissionFixture(t)
	snap, err := s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device)
	if err != nil {
		t.Fatal(err)
	}
	stageControlSnapshot(t, s, a, b, snap, "1")
	unpublishedOlmSibling(t, s, b)
	if r, err := s.FinalizeOlmAttempt(t.Context(), a, "1"); err != nil || r.State != "rejected" || r.Reason != "roster" {
		t.Fatalf("roster change must remain fenced, not fail unpublished: %+v %v", r, err)
	}
	snap, err = s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device)
	if err != nil {
		t.Fatal(err)
	}
	stageControlSnapshot(t, s, a, b, snap, "2")
	if r, err := s.FinalizeOlmAttempt(t.Context(), a, "2"); err != nil || r.State != "accepted" {
		t.Fatalf("refreshed targeted control: %+v %v", r, err)
	}
}

func TestOlmControlSnapshotAndFinalizeRequireCurrentTarget(t *testing.T) {
	for _, change := range []string{"revoked", "unpublished", "wrong-principal", "unapproved"} {
		t.Run(change, func(t *testing.T) {
			s, a, b, _, _ := admissionFixture(t)
			snap, err := s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device)
			if err != nil {
				t.Fatal(err)
			}
			stageControlSnapshot(t, s, a, b, snap, "1")
			// Mutate only after staging: finalize must independently recheck
			// target authority, even with a previously valid snapshot.
			want := ErrOlmAuthority
			switch change {
			case "revoked":
				_, err = s.RevokeDevice(t.Context(), b.Principal, b.Device, "user")
			case "unpublished":
				_, err = s.db.ExecContext(t.Context(), `DELETE FROM olm_directory_state WHERE device_id=$1`, b.Device)
				want = ErrOlmExhausted
			case "wrong-principal":
				_, err = s.db.ExecContext(t.Context(), `UPDATE user_devices SET principal=$2,first_device=FALSE,approval_signature=$3 WHERE device_id=$1`, b.Device, a.Principal, []byte("approved"))
			case "unapproved":
				_, err = s.db.ExecContext(t.Context(), `UPDATE user_devices SET first_device=FALSE,approval_signature=NULL WHERE device_id=$1`, b.Device)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device); !errors.Is(err, want) {
				t.Fatalf("snapshot accepted invalid target: %v", err)
			}
			if _, err = s.FinalizeOlmAttempt(t.Context(), a, "1"); !errors.Is(err, want) {
				t.Fatalf("finalize accepted invalid target: %v", err)
			}
		})
	}
}

func TestOlmControlSnapshotOwnDeviceTarget(t *testing.T) {
	s, a, _, _, _ := admissionFixture(t)
	d, _, bundle := olmFixture(t, s, a.Principal)
	if _, err := s.EndorseDevice(t.Context(), a.Principal, d.DeviceID, a.Device, []byte("approved")); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishOlmBundle(t.Context(), a.Principal, d.DeviceID, a.Server, bundle); err != nil {
		t.Fatal(err)
	}
	unpublishedOlmSibling(t, s, a)
	if snap, err := s.OlmControlSnapshot(t.Context(), a, a.Principal, d.DeviceID); err != nil || len(snap.Targets) != 2 || len(snap.Rosters) != 1 {
		t.Fatalf("own other device snapshot: %+v %v", snap, err)
	}
	if _, err := s.OlmControlSnapshot(t.Context(), a, a.Principal, a.Device); !errors.Is(err, admission.ErrInvalid) {
		t.Fatalf("local device cannot be its own control target: %v", err)
	}
}
