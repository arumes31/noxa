package connectionbenchmark

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
	"noxa/internal/netproto"
)

func benchmarkServer(t *testing.T, private, admit bool, moveBeforeAnswer ...bool) (Options, *atomic.Int32) {
	t.Helper()
	certServer := httptest.NewTLSServer(nil)
	certificate := certServer.TLS.Certificates[0]
	certServer.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(certificate.Certificate[0])
	options := Options{Address: listener.Addr().String(), Fingerprint: hex.EncodeToString(digest[:])}
	var offers atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		c := &control{Conn: conn}
		var pc *pion.PeerConnection
		defer func() {
			if pc != nil {
				_ = pc.Close()
			}
		}()
		for {
			frame, err := netproto.ReadFrame(c)
			if err != nil {
				return
			}
			switch netproto.MessageType(frame.Type) {
			case netproto.MsgAuthenticate:
				_ = c.write(netproto.MsgAuthResponse, netproto.AuthResponse{OK: admit, ClientID: "c-test", AuthorizationModel: netproto.AuthorizationModelRolesV1})
			case netproto.MsgServerInfoQuery:
				_ = c.write(netproto.MsgServerInfoResponse, netproto.ServerInfoResponse{EchoPrivate: private, EchoChannelID: 7, Version: "fixture"})
			case netproto.MsgJoinChannel:
				_ = c.write(netproto.MsgChannelJoined, netproto.ChannelJoined{ClientID: "c-test", ChannelID: 7})
			case netproto.MsgWebRTCOffer:
				offers.Add(1)
				if len(moveBeforeAnswer) > 0 && moveBeforeAnswer[0] {
					_ = c.write(netproto.MsgEvent, map[string]any{"type": "user_moved", "data": map[string]any{"client_id": "c-test", "channel_id": 9}})
					_ = c.write(netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "must not be accepted"})
					continue
				}
				var offer netproto.WebRTCOffer
				if netproto.Decode(frame, &offer) != nil {
					return
				}
				pc, err = pion.NewPeerConnection(pion.Configuration{})
				if err != nil {
					return
				}
				track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "c-test", "echo")
				if err != nil {
					return
				}
				if _, err := pc.AddTrack(track); err != nil {
					return
				}
				pc.OnTrack(func(remote *pion.TrackRemote, _ *pion.RTPReceiver) {
					for {
						packet, _, err := remote.ReadRTP()
						if err != nil {
							return
						}
						if track.WriteRTP(packet) != nil {
							return
						}
					}
				})
				if pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer.SDP}) != nil {
					return
				}
				answer, err := pc.CreateAnswer(nil)
				if err != nil {
					return
				}
				gathered := pion.GatheringCompletePromise(pc)
				if pc.SetLocalDescription(answer) != nil {
					return
				}
				<-gathered
				_ = c.write(netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: pc.LocalDescription().SDP})
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(9 * time.Second):
			t.Error("benchmark fixture leaked")
		}
	})
	return options, &offers
}

func TestBenchmarkActualLocalMediaRoundTrip(t *testing.T) {
	options, offers := benchmarkServer(t, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	result, err := run(ctx, options, nil, 300*time.Millisecond, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if offers.Load() != 1 || result.Sent < 5 || result.Returned == 0 || result.RoundTrip == nil || result.Protocol != "udp" {
		t.Fatalf("actual media did not return: %+v", result)
	}
}

func TestBenchmarkRejectsUnsafeServerBeforeMedia(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		private, admit, mismatch bool
	}{
		{name: "shared echo", admit: true}, {name: "guest rejected", private: true}, {name: "wrong certificate", private: true, admit: true, mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, offers := benchmarkServer(t, tc.private, tc.admit)
			if tc.mismatch {
				options.Fingerprint = strings.Repeat("00", 32)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := Run(ctx, options, nil); err == nil {
				t.Fatal("unsafe benchmark accepted")
			}
			if offers.Load() != 0 {
				t.Fatal("media was negotiated before safety checks")
			}
		})
	}
}

func TestBenchmarkRejectsMoveWhileWaitingForAnswer(t *testing.T) {
	options, _ := benchmarkServer(t, true, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := Run(ctx, options, nil); err == nil || !strings.Contains(err.Error(), "left private Echo Test") {
		t.Fatalf("membership change was swallowed: %v", err)
	}
}
