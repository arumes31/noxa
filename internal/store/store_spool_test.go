package store

import "testing"

func TestOfflineMessageKeyBindingMigration(t *testing.T) {
	for _, scenario := range []string{"fresh", "upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			s := testScratchStore(t)
			ctx := t.Context()
			if scenario == "upgrade" {
				applyPreLedgerMigrations(t, s, "038")
				if _, err := s.DB().ExecContext(ctx, `CREATE TABLE schema_migrations (
					filename TEXT PRIMARY KEY, checksum TEXT NOT NULL,
					applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					CONSTRAINT schema_migrations_checksum_sha256 CHECK (checksum ~ '^[0-9a-f]{64}$')
				)`); err != nil {
					t.Fatal(err)
				}
				for _, name := range migrationNames(t) {
					if name >= "038" {
						break
					}
					if _, err := s.DB().ExecContext(ctx, `INSERT INTO schema_migrations (filename, checksum)
						VALUES ($1, $2)`, name, migrationChecksum(t, name)); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			uid := seedScratchUser(t, s, "bound-spool")
			var legacyID int64
			if err := s.DB().QueryRowContext(ctx, `INSERT INTO offline_messages
				(from_user_id, to_user_id, from_unique_id, message)
				VALUES ($1, $1, 'legacy-sender', 'unchanged-ciphertext') RETURNING id`, uid).Scan(&legacyID); err != nil {
				t.Fatal(err)
			}
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				var body, senderKey, recipientKey, clientMsgID string
				if err := s.DB().QueryRowContext(ctx, `SELECT message, sender_public_key, recipient_public_key, client_msg_id
					FROM offline_messages WHERE id = $1`, legacyID).Scan(&body, &senderKey, &recipientKey, &clientMsgID); err != nil {
					t.Fatal(err)
				}
				if body != "unchanged-ciphertext" || senderKey != "" || recipientKey != "" || clientMsgID != "" {
					t.Fatalf("legacy ciphertext/binding changed: %q/%q/%q/%q", body, senderKey, recipientKey, clientMsgID)
				}
				if err := s.Migrate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOfflineMessageKeyBindingRoundTrip(t *testing.T) {
	s := testScratchStore(t)
	ctx := t.Context()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	sender := seedScratchUser(t, s, "sender")
	recipient := seedScratchUser(t, s, "recipient")
	other := seedScratchUser(t, s, "other")
	if err := s.SpoolMessage(ctx, sender, recipient, "sender-uid", "legacy-ciphertext"); err != nil {
		t.Fatal(err)
	}
	binding := DMKeyBinding{SenderPublicKey: "sender-device-key", RecipientPublicKey: "recipient-device-key", ClientMsgID: "client-message-id"}
	if err := s.SpoolMessage(ctx, sender, recipient, "sender-uid", "bound-ciphertext", binding); err != nil {
		t.Fatal(err)
	}
	if err := s.SpoolMessage(ctx, sender, recipient, "sender-uid", "invalid", binding, binding); err == nil {
		t.Fatal("multiple bindings were accepted")
	}
	if err := s.SpoolMessage(ctx, sender, other, "sender-uid", "other-ciphertext", binding); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingMessages(ctx, recipient)
	if err != nil || len(pending) != 2 {
		t.Fatalf("recipient pending = %+v, %v", pending, err)
	}
	legacy, bound := pending[0], pending[1]
	if legacy.Message != "legacy-ciphertext" || legacy.SenderPublicKey != "" || legacy.RecipientPublicKey != "" || legacy.ClientMsgID != "" {
		t.Fatalf("legacy row changed: %+v", legacy)
	}
	if bound.FromUserID != sender || bound.FromUniqueID != "sender-uid" || bound.FromName != "ledger-sender" ||
		bound.Message != "bound-ciphertext" || bound.SenderPublicKey != binding.SenderPublicKey ||
		bound.RecipientPublicKey != binding.RecipientPublicKey || bound.ClientMsgID != binding.ClientMsgID {
		t.Fatalf("bound row lost ciphertext or routing metadata: %+v", bound)
	}
	if err := s.MarkMessagesDelivered(ctx, []int64{bound.ID}); err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingMessages(ctx, recipient)
	if err != nil || len(pending) != 1 || pending[0].ID != legacy.ID {
		t.Fatalf("marking one row changed the remaining queue: %+v, %v", pending, err)
	}
	pending, err = s.PendingMessages(ctx, other)
	if err != nil || len(pending) != 1 || pending[0].Message != "other-ciphertext" {
		t.Fatalf("another recipient's queue changed: %+v, %v", pending, err)
	}
}
