package store

import (
	"errors"
	"testing"
)

// The offline spool is bounded per mailbox and per sender, so a registered
// sender cannot grow a victim's undelivered backlog without limit.
func TestSpoolMessageEnforcesMailboxQuota(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	oldMailbox, oldSender := MaxPendingPerMailbox, MaxPendingPerSender
	MaxPendingPerMailbox, MaxPendingPerSender = 5, 3
	t.Cleanup(func() { MaxPendingPerMailbox, MaxPendingPerSender = oldMailbox, oldSender })

	ctx := t.Context()
	attacker := seedScratchUser(t, s, "quota-attacker")
	friend := seedScratchUser(t, s, "quota-friend")
	other := seedScratchUser(t, s, "quota-other")
	victim := seedScratchUser(t, s, "quota-victim")

	for i := 0; i < MaxPendingPerSender; i++ {
		if err := s.SpoolMessage(ctx, attacker, victim, "a", "ciphertext"); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if err := s.SpoolMessage(ctx, attacker, victim, "a", "ciphertext"); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("sender share: err = %v, want ErrSpoolFull", err)
	}
	// Another device's mailbox is separate.
	if err := s.SpoolMessage(ctx, attacker, victim, "a", "copy", DMKeyBinding{RecipientDeviceID: "victim-phone"}); err != nil {
		t.Fatalf("device mailbox: %v", err)
	}
	// Other senders still reach the victim until the mailbox itself is full.
	for i := 0; i < 2; i++ {
		if err := s.SpoolMessage(ctx, friend, victim, "f", "ciphertext"); err != nil {
			t.Fatalf("friend message %d: %v", i, err)
		}
	}
	if err := s.SpoolMessage(ctx, other, victim, "o", "ciphertext"); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("mailbox: err = %v, want ErrSpoolFull", err)
	}
	// Delivered rows free their slots.
	if _, err := s.DB().ExecContext(ctx, `UPDATE offline_messages SET delivered_at = NOW() WHERE to_user_id = $1 AND from_user_id = $2`, victim, attacker); err != nil {
		t.Fatal(err)
	}
	if err := s.SpoolMessage(ctx, attacker, victim, "a", "ciphertext"); err != nil {
		t.Fatalf("after delivery: %v", err)
	}
	// Notes to self are bounded by the mailbox only.
	for i := 0; i < MaxPendingPerMailbox; i++ {
		if err := s.SpoolMessage(ctx, attacker, attacker, "a", "self"); err != nil {
			t.Fatalf("self message %d: %v", i, err)
		}
	}
	if err := s.SpoolMessage(ctx, attacker, attacker, "a", "self"); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("self mailbox: err = %v, want ErrSpoolFull", err)
	}
}

// Revoking a device drops the offline copies only that device could open, so
// they neither linger forever nor keep counting against its mailbox.
func TestRevokeDeviceDropsItsSpooledCopies(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	sender := seedScratchUser(t, s, "revoke-sender")
	recipient := seedScratchUser(t, s, "revoke-recipient")
	principal := "ledger_revoke-recipient"
	first := newTestDevice(principal, 0)
	first.UserID = recipient
	if _, err := s.AddDevice(ctx, first, 10); err != nil {
		t.Fatal(err)
	}
	phoneDevice := newTestDevice(principal, 1)
	phoneDevice.UserID = recipient
	phone, err := s.AddDevice(ctx, phoneDevice, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SpoolMessage(ctx, sender, recipient, "s", "account-wide"); err != nil {
		t.Fatal(err)
	}
	if err := s.SpoolMessage(ctx, sender, recipient, "s", "phone-copy", DMKeyBinding{RecipientDeviceID: phone.DeviceID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(ctx, principal, phone.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	var phoneRows, accountRows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE to_device_id = $2), COUNT(*) FILTER (WHERE to_device_id = '')
		FROM offline_messages WHERE to_user_id = $1`, recipient, phone.DeviceID).Scan(&phoneRows, &accountRows); err != nil {
		t.Fatal(err)
	}
	if phoneRows != 0 || accountRows != 1 {
		t.Fatalf("after revoke: phone rows = %d, account rows = %d", phoneRows, accountRows)
	}
}

// A channel holds at most MaxPinsPerChannel pins; re-pinning stays idempotent.
func TestPinChatMessageEnforcesChannelCap(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	var first int64
	for i := 0; i <= MaxPinsPerChannel; i++ {
		id, _, err := s.StoreChatMessage(ctx, 0, "author", "author", sealTest(t, testScopeKey(11), "pin me"), 1, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
		err = s.PinChatMessage(ctx, 0, id, "mod")
		if i < MaxPinsPerChannel && err != nil {
			t.Fatalf("pin %d: %v", i, err)
		}
		if i == MaxPinsPerChannel && !errors.Is(err, ErrPinLimit) {
			t.Fatalf("pin beyond cap: err = %v, want ErrPinLimit", err)
		}
	}
	if err := s.PinChatMessage(ctx, 0, first, "mod"); err != nil {
		t.Fatalf("re-pin at cap: %v", err)
	}
	pins, err := s.ChatPins(ctx, 0)
	if err != nil || len(pins) != MaxPinsPerChannel {
		t.Fatalf("pins = %d, %v", len(pins), err)
	}
}
