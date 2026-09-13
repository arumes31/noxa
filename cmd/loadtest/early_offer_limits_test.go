package main

import (
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"voicx/internal/netproto"
)

func TestInitialAnswerRejectsMultiplePendingOffers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, peer := net.Pipe()
		defer func() { _ = conn.Close(); _ = peer.Close() }()
		done := make(chan error, 1)
		go func() {
			for range 2 {
				if err := writeMsg(peer, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "pending"}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		start := time.Now()
		_, _, _, err := readWebRTCAnswer(conn, time.Second)
		if err == nil || !strings.Contains(err.Error(), "multiple WebRTC offers") {
			t.Fatalf("repeated early offers: %v", err)
		}
		if time.Since(start) != 0 {
			t.Fatal("waited for a timeout instead of bounding pending offers")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
