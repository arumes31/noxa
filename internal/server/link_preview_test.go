package server

import (
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestLinkPreviewRequiresAuthAndRejectsLocalTargets(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	unauth := dialRetry(t, env.addr)
	defer func() { _ = unauth.Close() }()
	send(t, unauth, netproto.MsgLinkPreviewRequest, netproto.LinkPreviewRequest{Provider: "web", Resource: "https://example.com/"})
	if got := readError(t, unauth); got.Code != errCodeNotAuthenticated {
		t.Fatalf("preauth fetch allowed: %+v", got)
	}
	client, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = client.Close() }()
	send(t, client, netproto.MsgLinkPreviewRequest, netproto.LinkPreviewRequest{Provider: "web", Resource: "https://127.0.0.1/"})
	if got := readError(t, client); got.Code != errCodeUnavailable || got.OriginType != uint16(netproto.MsgLinkPreviewRequest) {
		t.Fatalf("unsafe request: %+v", got)
	}
}
func TestLinkPreviewHasIndependentRateBudget(t *testing.T) {
	var budget sessionRequestBudget
	now := time.Now()
	for range 12 {
		if !budget.allow(netproto.MsgLinkPreviewRequest, now) {
			t.Fatal("unexpected exhaustion")
		}
	}
	if budget.allow(netproto.MsgLinkPreviewRequest, now) {
		t.Fatal("unbounded previews")
	}
	if !budget.allow(netproto.MsgPing, now) || !budget.allow(netproto.MsgChatSend, now) {
		t.Fatal("previews consumed unrelated budgets")
	}
}
