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
	if err := s.AddPasskey(ctx, Passkey{CredentialID: id, Principal: principal, UserHandle: handle, DeviceID: phone.DeviceID, Name: "iPhone", Credential: []byte(`{"id":"x"}`)}); err != nil {
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
	if err != nil || len(listed) != 1 || listed[0].LastUsedAt.IsZero() || string(listed[0].Credential) != `{"id": "y"}` {
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
