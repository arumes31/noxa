//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func privateGroupCallFixture(group netproto.Conversation) netproto.CallSession {
	now := time.Now().Unix()
	return netproto.CallSession{
		ID: "bb1bf0c3-16d7-45b2-80b9-4d2c0098cf3a", ConversationID: group.ID,
		Caller: "owner", CreatedAt: now, RingUntil: now + 30, Revision: 1,
		Participants: []netproto.CallParticipant{
			{UniqueID: "owner", ClientID: "owner-session", State: "accepted"},
			{UniqueID: "member", ClientID: "member-session", State: "ringing"},
		},
	}
}

func TestPrivateGroupCallSingleConnectionCommitsBeforeReturn(t *testing.T) {
	s, group := conversationTestStore(t)
	group = conversationTestChange(t, s, group, "owner", "invite", "member")
	group = conversationTestChange(t, s, group, "member", "accept", "")
	other := testAdditionalStore(t, s)
	s.DB().SetMaxOpenConns(1)
	s.DB().SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	want := privateGroupCallFixture(group)
	builds := 0
	got, err := s.StartPrivateGroupCall(ctx, "owner", group.ID, func(snapshot netproto.Conversation) (netproto.CallSession, error) {
		builds++
		if !reflect.DeepEqual(snapshot, group) {
			return netproto.CallSession{}, errors.New("builder did not receive the current membership snapshot")
		}
		return want, nil
	})
	if err != nil || builds != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("single-connection group call: builds=%d call=%+v error=%v", builds, got, err)
	}
	// A separate pool can only observe the call once its transaction commits.
	history, err := other.PrivateCallHistory(ctx, "member")
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0], want) {
		t.Fatalf("group call returned before persistence committed: history=%+v error=%v", history, err)
	}
}

func TestPrivateGroupCallDeniesPendingAndOutsiderBeforeBuilder(t *testing.T) {
	s, group := conversationTestStore(t)
	group = conversationTestChange(t, s, group, "owner", "invite", "member")
	for _, uid := range []string{"member", "outsider", ""} {
		built := false
		_, err := s.StartPrivateGroupCall(t.Context(), uid, group.ID, func(netproto.Conversation) (netproto.CallSession, error) {
			built = true
			return privateGroupCallFixture(group), nil
		})
		if !errors.Is(err, netproto.ErrConversationDenied) || built {
			t.Fatalf("uid=%q builder invoked=%t error=%v", uid, built, err)
		}
	}
	var rows int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM private_call_history`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("denied group starts persisted calls: rows=%d error=%v", rows, err)
	}
}

func TestPrivateCallHistoryOnlyActualParticipants(t *testing.T) {
	s, group := conversationTestStore(t)
	for _, uid := range []string{"member", "third"} {
		group = conversationTestChange(t, s, group, "owner", "invite", uid)
		group = conversationTestChange(t, s, group, uid, "accept", "")
	}
	want := privateGroupCallFixture(group)
	if _, err := s.StartPrivateGroupCall(t.Context(), "owner", group.ID, func(netproto.Conversation) (netproto.CallSession, error) { return want, nil }); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"owner", "member", "third", "outsider", ""} {
		history, err := s.PrivateCallHistory(t.Context(), uid)
		if err != nil {
			t.Fatal(err)
		}
		if uid == "owner" || uid == "member" {
			if len(history) != 1 || !reflect.DeepEqual(history[0], want) {
				t.Fatalf("participant %q lost call history: %+v", uid, history)
			}
		} else if len(history) != 0 {
			// In particular, membership in the private group alone must not
			// reveal a call that never included that user's live session.
			t.Fatalf("nonparticipant %q received call history: %+v", uid, history)
		}
	}
}

func TestPrivateGroupCallBuilderFailureDoesNotPersist(t *testing.T) {
	s, group := conversationTestStore(t)
	failed := errors.New("no available participants")
	if _, err := s.StartPrivateGroupCall(t.Context(), "owner", group.ID, func(netproto.Conversation) (netproto.CallSession, error) {
		return privateGroupCallFixture(group), failed
	}); !errors.Is(err, failed) {
		t.Fatalf("builder failure was not preserved: %v", err)
	}
	history, err := s.PrivateCallHistory(t.Context(), "owner")
	if err != nil || len(history) != 0 {
		t.Fatalf("failed call persisted history: %+v %v", history, err)
	}
}

func TestPrivateCallHistoryPagesAllAccessibleRows(t *testing.T) {
	s, group := conversationTestStore(t)
	for i := 1; i <= 55; i++ {
		call := privateGroupCallFixture(group)
		call.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		call.CreatedAt = 1000 // tied timestamps still paginate without gaps
		if err := s.SavePrivateCall(t.Context(), call); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.PrivateCallHistoryBefore(t.Context(), "owner", "")
	if err != nil || len(first) != 50 {
		t.Fatalf("first page: %d %v", len(first), err)
	}
	second, err := s.PrivateCallHistoryBefore(t.Context(), "owner", first[49].ID)
	if err != nil || len(second) != 5 {
		t.Fatalf("second page: %d %v", len(second), err)
	}
	seen := map[string]bool{}
	for _, call := range append(first, second...) {
		if seen[call.ID] {
			t.Fatal("repeated call", call.ID)
		}
		seen[call.ID] = true
	}
	denied, err := s.PrivateCallHistoryBefore(t.Context(), "outsider", first[49].ID)
	if err != nil || len(denied) != 0 {
		t.Fatalf("outsider received call history: %d %v", len(denied), err)
	}
	missing, err := s.PrivateCallHistoryBefore(t.Context(), "owner", "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if err != nil || len(missing) != 0 {
		t.Fatalf("unknown cursor repeated first page: %d %v", len(missing), err)
	}
}
