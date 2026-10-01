//go:build integration

package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func conversationTestStore(t *testing.T) (*Store, netproto.Conversation) {
	t.Helper()
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"owner", "member", "third", "outsider"} {
		if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES($1,$1)`, uid); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.CreateConversation(t.Context(), "owner", "Raid group")
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func conversationTestRead(t *testing.T, s *Store, uid string, request netproto.ConversationRequest) netproto.ConversationResult {
	t.Helper()
	var result netproto.ConversationResult
	if err := s.WithConversations(t.Context(), uid, request, func(got netproto.ConversationResult) error { result = got; return nil }); err != nil {
		t.Fatal(err)
	}
	return result
}

func conversationTestChange(t *testing.T, s *Store, c netproto.Conversation, uid, action, target string) netproto.Conversation {
	t.Helper()
	if _, err := s.ChangeConversation(t.Context(), uid, netproto.ConversationRequest{ID: c.ID, Revision: c.Revision, Action: action, Target: target}); err != nil {
		t.Fatal(err)
	}
	result := conversationTestRead(t, s, "owner", netproto.ConversationRequest{Action: "get", ID: c.ID})
	if len(result.Conversations) != 1 {
		t.Fatalf("conversation missing: %+v", result)
	}
	return result.Conversations[0]
}

func conversationTestSend(c netproto.Conversation, sequence int) netproto.ConversationRequest {
	envelopes := map[string]string{}
	for i, member := range c.Members {
		if !member.Pending {
			envelopes[member.UniqueID] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(sequence + i + 1)}, 64))
		}
	}
	return netproto.ConversationRequest{Action: "send", ID: c.ID, Revision: c.Revision, Message: &netproto.ConversationSend{Epoch: c.Epoch, Reference: fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence), Envelopes: envelopes}}
}

func TestConversationInvitationAcceptanceAndPrivacy(t *testing.T) {
	s, c := conversationTestStore(t)
	first := conversationTestSend(c, 1)
	if _, _, err := s.SendConversation(t.Context(), "owner", first); err != nil {
		t.Fatal(err)
	}
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	listed := conversationTestRead(t, s, "member", netproto.ConversationRequest{Action: "list"})
	if len(listed.Conversations) != 1 {
		t.Fatal("pending invitation is not listed")
	}
	pending, ok := listed.Conversations[0].Member("member")
	if !ok || !pending.Pending {
		t.Fatal("invitation silently joined member")
	}
	called := false
	err := s.WithConversations(t.Context(), "member", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(netproto.ConversationResult) error { called = true; return nil })
	if !errors.Is(err, netproto.ErrConversationDenied) || called {
		t.Fatalf("pending member read history: callback=%t error=%v", called, err)
	}
	if _, _, err := s.SendConversation(t.Context(), "member", conversationTestSend(c, 2)); !errors.Is(err, netproto.ErrConversationDenied) {
		t.Fatalf("pending member sent: %v", err)
	}
	outside := conversationTestRead(t, s, "outsider", netproto.ConversationRequest{Action: "list"})
	if len(outside.Conversations) != 0 || len(outside.Messages) != 0 {
		t.Fatal("outsider listed private conversation")
	}
	err = s.WithConversations(t.Context(), "outsider", netproto.ConversationRequest{Action: "get", ID: c.ID}, func(netproto.ConversationResult) error { called = true; return nil })
	if !errors.Is(err, netproto.ErrConversationDenied) || called {
		t.Fatalf("outsider got private conversation: %v", err)
	}
	c = conversationTestChange(t, s, c, "member", "accept", "")
	if got := conversationTestRead(t, s, "member", netproto.ConversationRequest{Action: "history", ID: c.ID}); len(got.Messages) != 0 {
		t.Fatal("new member read pre-acceptance history")
	}
	request := conversationTestSend(c, 3)
	id, recipients, err := s.SendConversation(t.Context(), "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(recipients)
	if !slices.Equal(recipients, []string{"member", "owner"}) {
		t.Fatalf("send recipients=%v", recipients)
	}
	for _, uid := range recipients {
		got := conversationTestRead(t, s, uid, netproto.ConversationRequest{Action: "history", ID: c.ID})
		if len(got.Messages) == 0 || got.Messages[0].ID != id || got.Messages[0].Body != request.Message.Envelopes[uid] || got.Messages[0].FromUniqueID != "owner" {
			t.Fatalf("recipient=%s history=%+v", uid, got.Messages)
		}
	}
}

func TestConversationRemovedAndRejoinedMemberHistory(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	if _, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(c, 1)); err != nil {
		t.Fatal(err)
	}
	c = conversationTestChange(t, s, c, "owner", "remove", "member")
	called := false
	err := s.WithConversations(t.Context(), "member", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(netproto.ConversationResult) error { called = true; return nil })
	if !errors.Is(err, netproto.ErrConversationDenied) || called {
		t.Fatalf("removed member got history: %v", err)
	}
	if _, _, err := s.SendConversation(t.Context(), "member", conversationTestSend(c, 2)); !errors.Is(err, netproto.ErrConversationDenied) {
		t.Fatalf("removed member sent: %v", err)
	}
	if _, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(c, 3)); err != nil {
		t.Fatal(err)
	}
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	if got := conversationTestRead(t, s, "member", netproto.ConversationRequest{Action: "history", ID: c.ID}); len(got.Messages) != 0 {
		t.Fatalf("rejoin exposed old or absent-period history: %+v", got.Messages)
	}
	request := conversationTestSend(c, 4)
	id, _, err := s.SendConversation(t.Context(), "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	got := conversationTestRead(t, s, "member", netproto.ConversationRequest{Action: "history", ID: c.ID})
	if len(got.Messages) != 1 || got.Messages[0].ID != id || got.Messages[0].Epoch != c.Epoch {
		t.Fatalf("rejoined member history=%+v", got.Messages)
	}
}

func TestConversationSendRequiresCurrentRecipientsAndEpoch(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	c = conversationTestChange(t, s, c, "owner", "invite", "third")
	for i, invalid := range []string{"missing recipient", "outsider recipient", "pending recipient", "old epoch", "old revision", "invalid base64", "short ciphertext", "large ciphertext"} {
		t.Run(invalid, func(t *testing.T) {
			request := conversationTestSend(c, i+1)
			switch invalid {
			case "missing recipient":
				delete(request.Message.Envelopes, "member")
			case "outsider recipient":
				request.Message.Envelopes["outsider"] = request.Message.Envelopes["owner"]
			case "pending recipient":
				request.Message.Envelopes["third"] = request.Message.Envelopes["owner"]
			case "old epoch":
				request.Message.Epoch--
			case "old revision":
				request.Revision--
			case "invalid base64":
				request.Message.Envelopes["member"] = "not base64!"
			case "short ciphertext":
				request.Message.Envelopes["member"] = base64.StdEncoding.EncodeToString(make([]byte, 39))
			case "large ciphertext":
				request.Message.Envelopes["member"] = base64.StdEncoding.EncodeToString(make([]byte, 18001))
			}
			if _, _, err := s.SendConversation(t.Context(), "owner", request); err == nil {
				t.Fatalf("accepted %s", invalid)
			}
			var messages, envelopes int
			if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM private_conversation_messages WHERE conversation_id=$1`, c.ID).Scan(&messages); err != nil {
				t.Fatal(err)
			}
			if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM private_conversation_envelopes`).Scan(&envelopes); err != nil {
				t.Fatal(err)
			}
			if messages != 0 || envelopes != 0 {
				t.Fatalf("invalid send left messages=%d envelopes=%d", messages, envelopes)
			}
		})
	}
}

func TestConversationSendDeduplicatesAtomically(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	request := conversationTestSend(c, 1)
	id, _, err := s.SendConversation(t.Context(), "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	other := testAdditionalStore(t, s)
	duplicateID, _, err := other.SendConversation(t.Context(), "owner", request)
	if err != nil || duplicateID != id {
		t.Fatalf("retry ID=%d original=%d error=%v", duplicateID, id, err)
	}
	var messageCount, envelopeCount int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM private_conversation_messages WHERE conversation_id=$1`, c.ID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM private_conversation_envelopes WHERE message_id=$1`, id).Scan(&envelopeCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 || envelopeCount != 2 {
		t.Fatalf("retry rows: messages=%d envelopes=%d", messageCount, envelopeCount)
	}
	for _, uid := range []string{"owner", "member"} {
		got := conversationTestRead(t, other, uid, netproto.ConversationRequest{Action: "history", ID: c.ID})
		if len(got.Messages) != 1 || got.Messages[0].Body != request.Message.Envelopes[uid] {
			t.Fatalf("persisted recipient envelope changed: %+v", got.Messages)
		}
	}
}

func TestConversationRemovalWaitsForHistoryDelivery(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	if _, _, err := s.SendConversation(t.Context(), "owner", conversationTestSend(c, 1)); err != nil {
		t.Fatal(err)
	}
	other := testAdditionalStore(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release, readDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer close(release)
	go func() {
		readDone <- s.WithConversations(ctx, "member", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(result netproto.ConversationResult) error {
			if len(result.Messages) != 1 {
				return fmt.Errorf("history missing before removal")
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-readDone:
		t.Fatalf("read did not enter callback: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	removeDone := make(chan error, 1)
	go func() {
		_, err := other.ChangeConversation(ctx, "owner", netproto.ConversationRequest{Action: "remove", ID: c.ID, Revision: c.Revision, Target: "member"})
		removeDone <- err
	}()
	select {
	case err := <-removeDone:
		t.Fatalf("removal completed before delivery: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	called := false
	err := other.WithConversations(ctx, "member", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(netproto.ConversationResult) error { called = true; return nil })
	if !errors.Is(err, netproto.ErrConversationDenied) || called {
		t.Fatalf("history delivered after successful removal: %v", err)
	}
	listed := conversationTestRead(t, other, "member", netproto.ConversationRequest{Action: "list"})
	if !reflect.DeepEqual(listed.Conversations, []netproto.Conversation{}) {
		t.Fatalf("removed conversation remains listed: %+v", listed.Conversations)
	}
}

func TestConversationCancelledHistoryRetainsLockUntilDeliveryReturns(t *testing.T) {
	s, c := conversationTestStore(t)
	c = conversationTestChange(t, s, c, "owner", "invite", "member")
	c = conversationTestChange(t, s, c, "member", "accept", "")
	other := testAdditionalStore(t, s)
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	requestCtx, cancelRequest := context.WithCancel(deadline)
	defer cancelRequest()
	cancelled, release, readDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer close(release)
	go func() {
		readDone <- s.WithConversations(requestCtx, "member", netproto.ConversationRequest{Action: "history", ID: c.ID}, func(netproto.ConversationResult) error {
			cancelRequest()
			close(cancelled)
			// A socket write can still be unwinding after request cancellation.
			// The membership lease must cover this entire callback lifetime.
			select {
			case <-release:
			case <-deadline.Done():
			}
			return requestCtx.Err()
		})
	}()
	select {
	case <-cancelled:
	case err := <-readDone:
		t.Fatalf("history never reached delivery: %v", err)
	case <-deadline.Done():
		t.Fatal(deadline.Err())
	}
	removeDone := make(chan error, 1)
	go func() {
		_, err := other.ChangeConversation(deadline, "owner", netproto.ConversationRequest{Action: "remove", ID: c.ID, Revision: c.Revision, Target: "member"})
		removeDone <- err
	}()
	select {
	case err := <-removeDone:
		t.Fatalf("request cancellation released membership lock before callback returned: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case release <- struct{}{}:
	case <-deadline.Done():
		t.Fatal(deadline.Err())
	}
	if err := <-readDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delivery result=%v", err)
	}
	if err := <-removeDone; err != nil {
		t.Fatalf("removal did not complete after callback returned: %v", err)
	}
}

func TestConversationConcurrentCrossInvitationsAvoidQuotaDeadlock(t *testing.T) {
	s, ownerGroup := conversationTestStore(t)
	memberGroup, err := s.CreateConversation(t.Context(), "member", "Member group")
	if err != nil {
		t.Fatal(err)
	}
	other := testAdditionalStore(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	// Pause both transactions after acquiring their target-user quota locks,
	// before membership inserts acquire FK key-share locks on both owners.
	// This makes the cross-invitation lock inversion deterministic.
	gateID := time.Now().UnixNano()%1_000_000_000 + 1_000_000_000
	trigger := fmt.Sprintf(`CREATE FUNCTION conversation_test_invite_gate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(%d::bigint);
  RETURN NEW;
END $$;
CREATE TRIGGER conversation_test_invite_gate BEFORE UPDATE ON private_conversations
FOR EACH ROW EXECUTE FUNCTION conversation_test_invite_gate()`, gateID)
	if _, err := s.DB().ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	gate, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, gateID); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, err := s.ChangeConversation(ctx, "owner", netproto.ConversationRequest{Action: "invite", ID: ownerGroup.ID, Revision: ownerGroup.Revision, Target: "member"})
		results <- err
	}()
	go func() {
		_, err := other.ChangeConversation(ctx, "member", netproto.ConversationRequest{Action: "invite", ID: memberGroup.ID, Revision: memberGroup.Revision, Target: "owner"})
		results <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := other.DB().QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=$1::oid AND NOT granted`, gateID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case err := <-results:
			t.Fatalf("invitation completed before reaching the concurrency barrier: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("both invitations did not reach the quota-lock barrier")
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("cross-invitation failed or deadlocked: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("cross-invitations failed to complete")
		}
	}
	for _, pair := range []struct {
		group          netproto.Conversation
		owner, invited string
	}{{ownerGroup, "owner", "member"}, {memberGroup, "member", "owner"}} {
		result := conversationTestRead(t, s, pair.owner, netproto.ConversationRequest{Action: "get", ID: pair.group.ID})
		if len(result.Conversations) != 1 {
			t.Fatal("group missing after cross-invitation")
		}
		invited, found := result.Conversations[0].Member(pair.invited)
		if !found || !invited.Pending {
			t.Fatalf("cross-invitation missing or auto-accepted: %+v", result.Conversations[0])
		}
	}
}
