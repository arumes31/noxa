package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"noxa/internal/netproto"
)

// The server may already be stable and create its next offer before its
// initial answer reaches the control socket. That offer must survive startup.
func TestOpusPublisherAnswersOfferArrivingBeforeInitialAnswer(t *testing.T) {
	remote, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	conn, peer := net.Pipe()
	defer func() { _ = conn.Close(); _ = peer.Close() }()
	if err := peer.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			frame, err := netproto.ReadFrame(peer)
			if err != nil {
				return err
			}
			var initial netproto.WebRTCOffer
			if err := netproto.Decode(frame, &initial); err != nil {
				return err
			}
			if err := remote.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: initial.SDP}); err != nil {
				return err
			}
			answer, err := remote.CreateAnswer(nil)
			if err != nil {
				return err
			}
			if err := remote.SetLocalDescription(answer); err != nil {
				return err
			}
			if _, err := remote.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
				return err
			}
			earlyOffer, err := remote.CreateOffer(nil)
			if err != nil {
				return err
			}
			if err := remote.SetLocalDescription(earlyOffer); err != nil {
				return err
			}
			if err := writeMsg(peer, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: earlyOffer.SDP}); err != nil {
				return err
			}
			if err := writeMsg(peer, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: answer.SDP}); err != nil {
				return err
			}
			if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				return err
			}
			reply, err := netproto.ReadFrame(peer)
			if err != nil {
				return fmt.Errorf("early server offer was not answered: %w", err)
			}
			if reply.Type != uint16(netproto.MsgWebRTCAnswer) {
				return fmt.Errorf("early offer reply type = %d", reply.Type)
			}
			var response netproto.WebRTCAnswer
			if err := netproto.Decode(reply, &response); err != nil {
				return err
			}
			return remote.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: response.SDP})
		}()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var st stats
	pc, err := startOpusPublisher(conn, nil, false, ctx, &st, 0)
	if err != nil {
		t.Fatalf("initial publisher setup: %v", err)
	}
	defer func() { _ = pc.Close() }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if remote.SignalingState() != webrtc.SignalingStateStable {
		t.Fatalf("server signaling state = %s", remote.SignalingState())
	}
	if len(pc.GetTransceivers()) != 2 {
		t.Fatal("early offer did not add the second publisher track")
	}
}
