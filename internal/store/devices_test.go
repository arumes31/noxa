package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestDevice(principal string, n int) NewDevice {
	return NewDevice{
		Principal:     principal,
		Kind:          DeviceKindWeb,
		Name:          fmt.Sprintf("Phone %d", n),
		Ed25519Public: fmt.Sprintf("ed25519-%s-%d-%d", principal, n, time.Now().UnixNano()),
		X25519Public:  fmt.Sprintf("x25519-%d", n),
	}
}

func TestDevicesFirstDeviceLimitAndKeyReuse(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("guest-principal-%d", time.Now().UnixNano())

	first := newTestDevice(principal, 0)
	first.Kind = DeviceKindDesktop
	d0, err := s.AddDevice(ctx, first, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !d0.FirstDevice || d0.UserID != 0 || !d0.Active() {
		t.Fatalf("first device = %+v", d0)
	}
	d1, err := s.AddDevice(ctx, newTestDevice(principal, 1), 3)
	if err != nil || d1.FirstDevice {
		t.Fatalf("second device = %+v, %v", d1, err)
	}
	if _, err := s.AddDevice(ctx, newTestDevice(principal, 2), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDevice(ctx, newTestDevice(principal, 3), 3); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("fourth device err = %v, want ErrDeviceLimit", err)
	}

	if _, err := s.RevokeDevice(ctx, principal, d1.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	reused := newTestDevice(principal, 4)
	reused.Ed25519Public = d1.Ed25519Public
	if _, err := s.AddDevice(ctx, reused, 3); !errors.Is(err, ErrDeviceKeyInUse) {
		t.Fatalf("revoked key reuse err = %v, want ErrDeviceKeyInUse", err)
	}
	if _, err := s.AddDevice(ctx, newTestDevice(principal, 5), 3); err != nil {
		t.Fatalf("revocation did not free a slot: %v", err)
	}
	if _, err := s.RevokeDevice(ctx, "someone-else", d0.DeviceID, DeviceRevokedByUser); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("cross-principal revoke err = %v", err)
	}

	byKey, err := s.DeviceByKey(ctx, d1.Ed25519Public)
	if err != nil || byKey.Active() || byKey.RevokedReason != DeviceRevokedByUser {
		t.Fatalf("revoked device by key = %+v, %v", byKey, err)
	}
	active, err := s.ListDevices(ctx, principal, false)
	if err != nil || len(active) != 3 || active[0].DeviceID != d0.DeviceID {
		t.Fatalf("active devices = %+v, %v", active, err)
	}
	all, err := s.ListDevices(ctx, principal, true)
	if err != nil || len(all) != 4 {
		t.Fatalf("all devices = %d, %v", len(all), err)
	}
}

func TestDevicesConcurrentAddRespectsLimit(t *testing.T) {
	s := pollTestStore(t)
	principal := fmt.Sprintf("race-principal-%d", time.Now().UnixNano())
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := s.AddDevice(t.Context(), newTestDevice(principal, n), 2)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrDeviceLimit) {
			t.Fatal(err)
		}
	}
	if ok != 2 {
		t.Fatalf("%d devices added under a limit of 2", ok)
	}
}

func TestDevicesResetRenameAndIdleExpiry(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("reset-principal-%d", time.Now().UnixNano())
	var ids []string
	for i := 0; i < 3; i++ {
		d, err := s.AddDevice(ctx, newTestDevice(principal, i), 10)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.DeviceID)
	}
	renamed, err := s.RenameDevice(ctx, principal, ids[1], "  Rowan's   iPhone ")
	if err != nil || renamed.Name != "Rowan's iPhone" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}

	// Age the second and third device; the first device never expires.
	old := time.Now().Add(-170 * 24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE user_devices SET last_seen_at=$2 WHERE principal=$1`, principal, old); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	warned, revoked, err := s.ExpireIdleDevices(ctx, now.Add(-166*24*time.Hour), now.Add(-180*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if countPrincipal(warned, principal) != 2 || countPrincipal(revoked, principal) != 0 {
		t.Fatalf("warned=%d revoked=%d", countPrincipal(warned, principal), countPrincipal(revoked, principal))
	}
	warned, _, err = s.ExpireIdleDevices(ctx, now.Add(-166*24*time.Hour), now.Add(-180*24*time.Hour))
	if err != nil || countPrincipal(warned, principal) != 0 {
		t.Fatalf("devices warned twice: %d, %v", countPrincipal(warned, principal), err)
	}
	if err := s.TouchDevice(ctx, ids[1], now); err != nil {
		t.Fatal(err)
	}
	_, revoked, err = s.ExpireIdleDevices(ctx, now.Add(-166*24*time.Hour), now.Add(-160*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if countPrincipal(revoked, principal) != 1 || revoked[indexPrincipal(revoked, principal)].DeviceID != ids[2] {
		t.Fatalf("idle revocation = %+v", revoked)
	}

	reset, err := s.ResetDevices(ctx, principal, ids[0], DeviceRevokedByAdminReset)
	if err != nil || len(reset) != 1 || reset[0].DeviceID != ids[1] {
		t.Fatalf("reset = %+v, %v", reset, err)
	}
	active, err := s.ListDevices(ctx, principal, false)
	if err != nil || len(active) != 1 || active[0].DeviceID != ids[0] {
		t.Fatalf("after reset = %+v, %v", active, err)
	}
}

func countPrincipal(devices []Device, principal string) int {
	n := 0
	for _, d := range devices {
		if d.Principal == principal {
			n++
		}
	}
	return n
}

func indexPrincipal(devices []Device, principal string) int {
	for i, d := range devices {
		if d.Principal == principal {
			return i
		}
	}
	return -1
}

func TestNormalizeDeviceName(t *testing.T) {
	if got := NormalizeDeviceName("   "); got != "Device" {
		t.Fatalf("empty name = %q", got)
	}
	long := NormalizeDeviceName(string(make([]rune, 0)) + fmt.Sprintf("%070d", 0))
	if len([]rune(long)) != 64 {
		t.Fatalf("long name length = %d", len([]rune(long)))
	}
}
