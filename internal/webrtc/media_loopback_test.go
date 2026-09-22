package webrtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// Exercise the production Engine and Router over actual ICE/DTLS/SRTP.
// Both automatic UDP sockets and the shared listener are covered.
func TestMediaEgressLoopbackSockets(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "automatic"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			network := NetworkConfig{}
			if shared {
				network.UDPAddr = "0.0.0.0:0"
			}
			engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, network)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.Close() })
			// Local transport needs only host candidates; avoid depending on a
			// public STUN service or waiting for an unreachable one to time out.
			engine.iceServers = nil
			peer, err := engine.NewPeerConnection("listener")
			if err != nil {
				t.Fatal(err)
			}
			sender := peer.pc
			router := NewRouter(testLogger())
			router.JoinChannel(1, "speaker")
			router.JoinChannel(1, "listener")
			if err := router.AttachPeer("listener", peer); err != nil {
				t.Fatal(err)
			}
			router.PrepareSubscriber("listener")
			var guardCalls atomic.Int64
			router.SetMediaGuard(func(delivery MediaDelivery, write func() error) error {
				guardCalls.Add(1)
				if delivery.RecipientID != "listener" || delivery.SenderID != "speaker" {
					t.Errorf("unexpected media delivery: %+v", delivery)
				}
				return write()
			})
			receiverSettings := pion.SettingEngine{}
			receiverSettings.SetInterfaceFilter(usableICEInterface)
			receiverAPI := pion.NewAPI(pion.WithSettingEngine(receiverSettings))
			receiver, err := receiverAPI.NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = receiver.Close() })
			received := make(chan *rtp.Packet, 4)
			receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
				packet, _, err := track.ReadRTP()
				if err == nil {
					received <- packet
				}
			})
			gather := func(pc *pion.PeerConnection, description pion.SessionDescription) {
				t.Helper()
				done := pion.GatheringCompletePromise(pc)
				if err := pc.SetLocalDescription(description); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("ICE gathering timeout")
				}
			}
			offer, err := sender.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gather(sender, offer)
			if err = receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			answer, err := receiver.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gather(receiver, answer)
			if err = sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			var seq uint16
			for {
				select {
				case packet := <-received:
					if len(packet.CSRC) != 1 || packet.CSRC[0] != 123 || len(packet.Payload) != 3 {
						t.Fatalf("RTP corrupted: %+v", packet)
					}
					if guardCalls.Load() <= int64(seq) {
						t.Fatal("installed egress did not recheck authorization after enqueue")
					}
					return
				case <-tick.C:
					seq++
					router.ForwardRTP("speaker", SlotMic, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: uint32(seq) * 960, CSRC: []uint32{123}}, Payload: []byte{0xf8, 0xff, 0xfe}})
				case <-deadline.C:
					t.Fatalf("media did not cross actual ICE/DTLS/SRTP stack: sender=%s receiver=%s", sender.ConnectionState(), receiver.ConnectionState())
				}
			}
		})
	}
}

func TestEngineCannotSelectUncredentialedServerTURN(t *testing.T) {
	e, err := New(testLogger(), []string{"turn:127.0.0.1:3478"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	if peer, err := e.NewPeerConnection("turn"); err == nil {
		_ = peer.Close()
		t.Fatal("server TURN unexpectedly negotiated without credentials")
	}
}
