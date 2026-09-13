package main

import (
	"net"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"voicx/internal/netproto"
)

func TestAnswerRenegotiationAcceptsAdditionalPublisher(t *testing.T) {
	server, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	client, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	reader, writer := net.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	_ = reader.SetDeadline(time.Now().Add(10 * time.Second))
	_ = writer.SetDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 2; i++ {
		if _, err := server.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
			t.Fatal(err)
		}
		offer, err := server.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.SetLocalDescription(offer); err != nil {
			t.Fatal(err)
		}
		frame, err := netproto.Encode(netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: offer.SDP})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- answerRenegotiation(writer, client, frame) }()
		answerFrame, err := netproto.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if answerFrame.Type != uint16(netproto.MsgWebRTCAnswer) {
			t.Fatalf("reply type %d", answerFrame.Type)
		}
		var answer netproto.WebRTCAnswer
		if err := netproto.Decode(answerFrame, &answer); err != nil {
			t.Fatal(err)
		}
		if err := server.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if client.SignalingState() != webrtc.SignalingStateStable {
			t.Fatalf("client signaling = %s", client.SignalingState())
		}
	}
	if len(client.GetTransceivers()) != 2 {
		t.Fatal("renegotiation omitted additional publisher")
	}
}
