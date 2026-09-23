package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/netproto"
)

type readPositionTestStore struct{ *privateCallTestStore }

func (*readPositionTestStore) MarkConversationRead(context.Context, string, string, int64) error {
	return nil
}

func TestConversationReadPositionsHaveIndependentRateLimit(t *testing.T) {
	env, backend := privateCallsTestEnv(t)
	defer env.stop()
	env.srv.deps.Chat = &readPositionTestStore{backend}
	env.srv.chatRate = newChatRateLimiter(1, time.Hour)
	env.srv.conversationReadRate = newChatRateLimiter(2, time.Hour)
	client, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = client.Close() }()
	request := netproto.ConversationRequest{Action: "mark_read", ID: "eb185ca2-1034-489d-a6ce-0a6f90c57101", ReadMessageID: 1}
	for range 2 {
		send(t, client, netproto.MsgConversationRequest, request)
		var result netproto.ConversationResult
		if err := netproto.Decode(readOfType(t, client, netproto.MsgConversationResult), &result); err != nil || result.Action != "mark_read" {
			t.Fatalf("read acknowledgment=%+v error=%v", result, err)
		}
	}
	send(t, client, netproto.MsgConversationRequest, request)
	if result := readError(t, client); result.Code != errCodeMalformed || result.Message != "group read rate limit exceeded" {
		t.Fatalf("read limiter did not reject excess writes: %+v", result)
	}
	if !env.srv.chatRate.allow("user-uid", time.Now()) {
		t.Fatal("reading messages consumed the user's chat budget")
	}
}
