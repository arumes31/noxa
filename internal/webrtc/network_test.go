package webrtc

import (
	"net"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func TestSharedUDPPortCandidatesAndCleanup(t *testing.T) {
	e, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{
		UDPAddr: ":0", ExternalIPs: []string{"203.0.113.10", "100.103.150.8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	addr := e.udpMux.GetListenAddresses()[0].String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		peer, err := e.NewPeerConnection(id)
		if err != nil {
			t.Fatal(err)
		}
		pc := peer.pc
		if _, err := pc.CreateDataChannel("probe", nil); err != nil {
			t.Fatal(err)
		}
		done := pion.GatheringCompletePromise(pc)
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("ICE gathering timed out")
		}
		for _, ip := range []string{"203.0.113.10", "100.103.150.8"} {
			if !strings.Contains(pc.LocalDescription().SDP, ip+" "+port+" typ host") {
				t.Fatalf("missing forwarded UDP candidate: %s", pc.LocalDescription().SDP)
			}
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatalf("shared UDP socket was not released: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedUDPPortBindFailure(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: conn.LocalAddr().String()}); err == nil {
		t.Fatal("expected occupied UDP port to fail startup")
	}
}
