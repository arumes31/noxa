package webrtc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	pion "github.com/pion/webrtc/v4"
)

// Windows can report zero elapsed time for an immediate loopback STUN exchange.
// Delay actual UDP sends slightly so this integration fixture measures a real,
// nonzero round trip rather than injecting synthetic stats.
type repairRTTDelayedPacketConn struct{ net.PacketConn }

func (c *repairRTTDelayedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	time.Sleep(2 * time.Millisecond)
	return c.PacketConn.WriteTo(p, addr)
}

// Exercise the production stats bridge without external STUN or an application
// client. Opening SCTP provides an event barrier after local ICE/DTLS succeeds.
func TestVideoRepairRTTPionLoopbackFirstGap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newPeer := func() *pion.PeerConnection {
		t.Helper()
		setting := pion.SettingEngine{}
		setting.SetIncludeLoopbackCandidate(true)
		setting.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
		setting.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
		conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: &repairRTTDelayedPacketConn{PacketConn: conn}})
		t.Cleanup(func() {
			if err := mux.Close(); err != nil {
				t.Error(err)
			}
		})
		setting.SetICEUDPMux(mux)
		api := pion.NewAPI(pion.WithSettingEngine(setting))
		pc, err := api.NewPeerConnection(pion.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pc.Close(); err != nil {
				t.Error(err)
			}
		})
		return pc
	}
	first, second := newPeer(), newPeer()
	channel, err := first.CreateDataChannel("repair-rtt-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	channel.OnOpen(func() { close(opened) })
	gather := func(pc *pion.PeerConnection, description pion.SessionDescription) {
		t.Helper()
		done := pion.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(description); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("loopback ICE gathering exceeded deadline")
		}
	}
	offer, err := first.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(first, offer)
	if err := second.SetRemoteDescription(*first.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := second.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(second, answer)
	if err := first.SetRemoteDescription(*second.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("loopback SCTP did not open")
	}
	// The answerer matches the server role for an upstream browser offer.
	if second.ConnectionState() != pion.PeerConnectionStateConnected {
		t.Fatal("SCTP open without connected peer")
	}
	selected, ok := second.SCTP().Transport().ICETransport().GetSelectedCandidatePairStats()
	if !ok || selected.ResponsesReceived == 0 {
		t.Fatal("connected local pair lacks STUN response")
	}
	// Pinned ICE v4.4.7 leaves this field unimplemented; its converted zero
	// time is nonpositive, not a usable current response timestamp.
	if selected.LastResponseTimestamp > 0 {
		t.Fatal("selected-only getter changed; reassess full-report requirement")
	}
	peer := &PeerConnectionWrapper{pc: second, closed: make(chan struct{})}
	t.Cleanup(func() { close(peer.closed) })
	future := peer.RequestVideoRepairRTT() // first request: no preceding gaps/cache
	if future == nil {
		t.Fatal("first gap did not start lookup")
	}
	select {
	case <-future.done:
	case <-ctx.Done():
		t.Fatal("production RTT lookup exceeded deadline")
	}
	if future.rtt <= 0 || future.measuredAt.IsZero() {
		t.Logf("selected state=%v responses=%d rtt=%g local=%q remote=%q", selected.State, selected.ResponsesReceived, selected.CurrentRoundTripTime, selected.LocalCandidateID, selected.RemoteCandidateID)
		for _, raw := range second.GetStats() {
			if stat, ok := raw.(pion.ICECandidatePairStats); ok {
				t.Logf("full pair state=%v responses=%d rtt=%g response=%g now=%d matches=%v", stat.State, stat.ResponsesReceived, stat.CurrentRoundTripTime, stat.LastResponseTimestamp, time.Now().UnixMilli(), stat.LocalCandidateID == selected.LocalCandidateID && stat.RemoteCandidateID == selected.RemoteCandidateID)
			}
		}
		t.Fatal("production lookup failed to obtain actual full-report response measurement")
	}
	age := time.Since(future.measuredAt)
	if age < 0 || age > videoRepairRTTFreshness {
		t.Fatalf("actual response age %v is not fresh", age)
	}
	if future.requestedAt.After(time.Now()) {
		t.Fatal("request timestamp is in the future")
	}
	t.Logf("first-gap RTT=%v actual-response-age=%v; selected-only timestamp unavailable", future.rtt, age)
}
