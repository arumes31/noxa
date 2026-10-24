package store

import (
	"bytes"
	"encoding/base64"
	"noxa/internal/netproto"
	"testing"
)

func TestConversationHistoryRetainsRevokedSenderKey(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO users(unique_id,nickname) VALUES('history-owner','Owner')`); err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateConversation(ctx, "history-owner", "History")
	if err != nil {
		t.Fatal(err)
	}
	first := newTestDevice("history-owner", 0)
	first.Kind = DeviceKindDesktop
	root, err := s.AddDevice(ctx, first, 10)
	if err != nil {
		t.Fatal(err)
	}
	phoneSpec := newTestDevice("history-owner", 1)
	phoneSpec.ApprovedBy = root.DeviceID
	phoneSpec.ApprovalSignature = bytes.Repeat([]byte{1}, 64)
	phone, err := s.AddDevice(ctx, phoneSpec, 10)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64))
	request := netproto.ConversationRequest{Action: "send", ID: c.ID, Revision: c.Revision, DeviceID: phone.DeviceID, Message: &netproto.ConversationSend{Epoch: c.Epoch, Reference: "history-test", Envelopes: map[string]string{"history-owner": body}}}
	if _, _, err = s.SendConversation(ctx, "history-owner", request); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevokeDevice(ctx, "history-owner", phone.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	s.DB().SetMaxOpenConns(1) // Proof loading must share the history transaction.
	err = s.WithConversations(ctx, "history-owner", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(result netproto.ConversationResult) error {
		if len(result.Messages) != 1 || result.Messages[0].SenderDeviceKey != phone.X25519Public {
			t.Errorf("historical sender key lost: %+v", result.Messages)
		}
		if len(result.Messages) == 1 {
			chain := result.Messages[0].SenderDeviceChain
			if len(chain) != 2 || chain[0].DeviceID != phone.DeviceID || chain[1].DeviceID != root.DeviceID || len(chain[0].ApprovalSignature) != 64 {
				t.Errorf("incomplete historical chain: %+v", chain)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
