package server

import (
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestSessionRequestBudgets(t *testing.T) {
	var a, b sessionRequestBudget
	now := time.Now()
	for i := 0; i < 24; i++ {
		if !a.allow(netproto.MsgChatHistory, now) {
			t.Fatal("legitimate history burst rejected")
		}
	}
	for _, mt := range []netproto.MessageType{netproto.MsgChatHistory, netproto.MsgJoinChannel, netproto.MsgFileList, netproto.MsgFileTransferInit} {
		if a.allow(mt, now) {
			t.Fatalf("heavy work admitted: %v", mt)
		}
	}
	if !b.allow(netproto.MsgChatHistory, now) {
		t.Fatal("another session affected")
	}
	for i := 0; i < 256; i++ {
		if !a.allow(netproto.MsgICECandidate, now) {
			t.Fatal("ICE burst affected by history")
		}
	}
	if a.allow(netproto.MsgICECandidate, now) {
		t.Fatal("unbounded ICE")
	}
	for i := 0; i < 64; i++ {
		if !a.allow(netproto.MsgWebRTCOffer, now) {
			t.Fatal("offer burst affected by ICE")
		}
	}
	if a.allow(netproto.MsgWebRTCOffer, now) {
		t.Fatal("unbounded offers")
	}
	if !a.allow(netproto.MsgPing, now) {
		t.Fatal("heartbeat throttled")
	}
	if !a.allow(netproto.MsgChatHistory, now.Add(time.Second)) {
		t.Fatal("budget did not refill")
	}
}

func TestSpeechSynthesisBudgetIsSeparateAndSlow(t *testing.T) {
	var b sessionRequestBudget
	now := time.Now()
	for range 10 {
		if !b.allowSpeechSynthesis(now) {
			t.Fatal("burst denied")
		}
	}
	if b.allowSpeechSynthesis(now) || !b.allow(netproto.MsgSpokenNameGet, now) {
		t.Fatal("synthesis budget not exhausted, or it consumed request tokens")
	}
	if b.allowSpeechSynthesis(now.Add(30*time.Second)) || !b.allowSpeechSynthesis(now.Add(61*time.Second)) {
		t.Fatal("synthesis refills about once a minute")
	}
}
