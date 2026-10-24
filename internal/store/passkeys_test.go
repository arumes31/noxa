package store

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPasskeysFollowTheirDevice(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("passkey-principal-%d", time.Now().UnixNano())
	desktop, err := s.AddDevice(ctx, newTestDevice(principal, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := s.AddDevice(ctx, newTestDevice(principal, 1), 10)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := s.PasskeyUserHandle(ctx, principal)
	if err != nil || len(handle) != 32 {
		t.Fatalf("new handle = %x, %v", handle, err)
	}
	id := []byte(principal + "-credential")
	if err := s.AddPasskey(ctx, Passkey{CredentialID: id, Principal: principal, UserHandle: handle, DeviceID: phone.DeviceID, Name: "iPhone", Credential: []byte(`{"id":"x"}`), DMCertificate: []byte(`{"signed":"delegation"}`)}); err != nil {
		t.Fatal(err)
	}
	again, err := s.PasskeyUserHandle(ctx, principal)
	if err != nil || !bytes.Equal(again, handle) {
		t.Fatalf("handle changed: %x, %v", again, err)
	}
	if err := s.AddPasskey(ctx, Passkey{CredentialID: []byte("other"), Principal: "someone-else", UserHandle: handle, DeviceID: phone.DeviceID, Name: "x", Credential: []byte(`{}`)}); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("passkey on another account's device err = %v", err)
	}
	byHandle, err := s.PasskeysByHandle(ctx, handle)
	if err != nil || len(byHandle) != 1 || byHandle[0].Principal != principal {
		t.Fatalf("by handle = %+v, %v", byHandle, err)
	}
	if err := s.RecordPasskeyUse(ctx, id, []byte(`{"id":"y"}`)); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Passkeys(ctx, principal)
	if err != nil || len(listed) != 1 || listed[0].LastUsedAt.IsZero() || string(listed[0].Credential) != `{"id": "y"}` || string(listed[0].DMCertificate) != `{"signed":"delegation"}` {
		t.Fatalf("passkeys = %+v, %v", listed, err)
	}

	// Revoking the registering device removes its passkeys in the same statement.
	if _, err := s.RevokeDevice(ctx, principal, phone.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.Passkeys(ctx, principal); err != nil || len(listed) != 0 {
		t.Fatalf("passkeys after revoke = %+v, %v", listed, err)
	}
	if err := s.AddPasskey(ctx, Passkey{CredentialID: []byte(principal + "-late"), Principal: principal, UserHandle: handle, DeviceID: phone.DeviceID, Name: "x", Credential: []byte(`{}`)}); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("passkey on a revoked device err = %v", err)
	}
	if err := s.DeletePasskey(ctx, principal, id); !errors.Is(err, ErrPasskeyNotFound) {
		t.Fatalf("delete of removed passkey err = %v", err)
	}
	_ = desktop
}

func TestSignOutRetainsPasskeyUntilExplicitRevocation(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%v", reset), func(t *testing.T) {
			s := pollTestStore(t)
			ctx := t.Context()
			principal := fmt.Sprintf("signout-%d", time.Now().UnixNano())
			desktop, err := s.AddDevice(ctx, newTestDevice(principal, 0), 10)
			if err != nil {
				t.Fatal(err)
			}
			phone, err := s.AddDevice(ctx, newTestDevice(principal, 1), 10)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := s.PasskeyUserHandle(ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range []Device{desktop, phone} {
				if err := s.AddPasskey(ctx, Passkey{CredentialID: []byte(d.DeviceID), Principal: principal, UserHandle: handle, DeviceID: d.DeviceID, Name: "test", Credential: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SavePushSubscription(ctx, PushSubscription{DeviceID: phone.DeviceID, Principal: principal, Endpoint: "https://push.example/" + principal, P256dh: []byte{4, 1}, Auth: []byte("0123456789abcdef")}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutHistoryChunk(ctx, principal, HistoryChunk{DeviceID: phone.DeviceID, FromDeviceID: desktop.DeviceID, Seq: 0, Total: 1, Data: []byte("encrypted")}, 1024); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SignOutDevice(ctx, "other-account", phone.DeviceID); !errors.Is(err, ErrDeviceNotFound) {
				t.Fatalf("cross-account signout: %v", err)
			}
			signedOut, err := s.SignOutDevice(ctx, principal, phone.DeviceID)
			if err != nil || signedOut.Active() {
				t.Fatalf("signout: %+v, %v", signedOut, err)
			}
			if rows, err := s.PasskeysByHandle(ctx, handle); err != nil || len(rows) != 2 {
				t.Fatalf("retained keys: %d, %v", len(rows), err)
			}
			if subs, err := s.PushSubscriptions(ctx, principal); err != nil || len(subs) != 0 {
				t.Fatalf("push not removed: %v", err)
			}
			var pending int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM device_history_chunks WHERE device_id=$1`, phone.DeviceID).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("history not removed: %d, %v", pending, err)
			}
			if _, err := s.AddDevice(ctx, NewDevice{Principal: principal, Kind: DeviceKindWeb, Ed25519Public: phone.Ed25519Public, X25519Public: phone.X25519Public}, 10); !errors.Is(err, ErrDeviceKeyInUse) {
				t.Fatalf("old key reused: %v", err)
			}
			if reset {
				_, err = s.ResetDevices(ctx, principal, desktop.DeviceID, DeviceRevokedByAdminReset)
			} else {
				_, err = s.RevokeDevice(ctx, principal, phone.DeviceID, DeviceRevokedByUser)
			}
			if err != nil {
				t.Fatal(err)
			}
			rows, err := s.Passkeys(ctx, principal)
			if err != nil || len(rows) != 1 || rows[0].DeviceID != desktop.DeviceID {
				t.Fatalf("revoke/reset did not remove signed-out device credential or removed kept credential: %+v, %v", rows, err)
			}
		})
	}
}
