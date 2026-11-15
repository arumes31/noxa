package store

import (
	"errors"
	"testing"
)

// idleOlmDevice backdates a device's last activity past olmUnpublishedIdle.
func idleOlmDevice(t *testing.T, s *Store, deviceID string) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE user_devices SET last_seen_at = now() - interval '30 days' WHERE device_id=$1`, deviceID); err != nil {
		t.Fatal(err)
	}
}

// An approved device that never published and has been idle for weeks (e.g. a
// web session from before secure messaging) must not block every conversation
// with its owner; a recently seen unpublished device still does.
func TestOlmSnapshotSkipsIdleUnpublishedDevices(t *testing.T) {
	s, a, b, _, _ := admissionFixture(t)
	stale := unpublishedOlmSibling(t, s, b)
	if _, err := s.OlmSnapshot(t.Context(), a, "direct", b.Principal, ""); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("a recently seen unpublished device must still be required: %v", err)
	}
	idleOlmDevice(t, s, stale.DeviceID)
	snap, err := s.OlmSnapshot(t.Context(), a, "direct", b.Principal, "")
	if err != nil {
		t.Fatalf("an idle unpublished device must not block the conversation: %v", err)
	}
	for _, target := range snap.Targets {
		if target.Device == stale.DeviceID {
			t.Fatalf("idle unpublished device listed as a target: %+v", snap.Targets)
		}
	}
	if len(snap.Targets) != 2 {
		t.Fatalf("want the two published devices, got %+v", snap.Targets)
	}
	// Once it signs in again it is seen recently and required again.
	if _, err := s.db.ExecContext(t.Context(), `UPDATE user_devices SET last_seen_at = now() WHERE device_id=$1`, stale.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OlmSnapshot(t.Context(), a, "direct", b.Principal, ""); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("a returning unpublished device must be required again: %v", err)
	}
}

// A targeted control to an idle unpublished device fails as an authority error
// (no such usable device), not as a retryable exhausted roster.
func TestOlmControlSnapshotIdleUnpublishedTarget(t *testing.T) {
	s, a, b, _, _ := admissionFixture(t)
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM olm_directory_state WHERE device_id=$1`, b.Device); err != nil {
		t.Fatal(err)
	}
	idleOlmDevice(t, s, b.Device)
	if _, err := s.OlmControlSnapshot(t.Context(), a, b.Principal, b.Device); !errors.Is(err, ErrOlmAuthority) {
		t.Fatalf("control to an idle unpublished device: %v", err)
	}
	// A peer whose only device is idle and unpublished has no usable device.
	if _, err := s.OlmSnapshot(t.Context(), a, "direct", b.Principal, ""); !errors.Is(err, ErrOlmAuthority) {
		t.Fatalf("chat with a peer that has no usable device: %v", err)
	}
}
