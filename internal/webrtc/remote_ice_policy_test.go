package webrtc

import (
	"net"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func TestRemoteICEPolicyAddresses(t *testing.T) {
	policy, err := remoteICEPolicy([]string{"192.168.5.0/24", "fd00:123::/48", "100.100.0.0/16"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "172.16.0.3", "192.168.6.1", "169.254.169.254", "fe80::1", "ff02::1", "224.0.0.1", "0.0.0.0", "::", "255.255.255.255", "240.0.0.1", "0.1.2.3", "100.64.0.1", "fd00:124::1"} {
		if policy(net.ParseIP(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	for _, ip := range []string{"192.168.5.1", "::ffff:192.168.5.1", "fd00:123::1", "100.100.0.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !policy(net.ParseIP(ip)) {
			t.Errorf("blocked %s", ip)
		}
	}
	broad, _ := remoteICEPolicy([]string{"0.0.0.0/0", "::/0"}, true)
	for _, ip := range []string{"169.254.169.254", "fe80::1", "224.0.0.1", "0.0.0.0"} {
		if broad(net.ParseIP(ip)) {
			t.Errorf("exception admitted %s", ip)
		}
	}
	if !broad(net.ParseIP("127.0.0.1")) {
		t.Fatal("explicit local listener blocked")
	}
	for _, raw := range []string{"garbage", "10.0.0.1", "::ffff:10.0.0.0/104"} {
		if _, err := remoteICEPolicy([]string{raw}, false); err == nil {
			t.Fatalf("invalid CIDR accepted: %s", raw)
		}
	}
}

// Exercise Pion's actual admission after both SDP and queued trickle parsing.
// The remote candidate statistics must never contain denied destinations.
func TestRemoteICEPolicySignaling(t *testing.T) {
	for _, answer := range []bool{false, true} {
		t.Run(map[bool]string{false: "offer", true: "answer"}[answer], func(t *testing.T) {
			engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: ":0", ExternalIPs: []string{"203.0.113.10"}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = engine.Close() }()
			peer, err := engine.NewPeerConnection("policy")
			if err != nil {
				t.Fatal(err)
			}
			client, err := pion.NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if _, err = client.CreateDataChannel("test", nil); err != nil {
				t.Fatal(err)
			}
			candidates := "a=candidate:11 1 udp 2130706431 169.254.169.254 40001 typ host\r\n" +
				"a=candidate:12 1 udp 2130706430 10.0.0.1 40002 typ host\r\n" +
				"a=candidate:13 1 udp 2130706429 secret.local 40003 typ host\r\n" +
				"a=candidate:14 1 udp 2130706428 198.51.100.10 40004 typ host\r\n"
			if answer {
				if _, err = peer.pc.CreateDataChannel("test", nil); err != nil {
					t.Fatal(err)
				}
				offer, err := peer.CreateOffer()
				if err != nil {
					t.Fatal(err)
				}
				if err = client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer}); err != nil {
					t.Fatal(err)
				}
				response, err := client.CreateAnswer(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = peer.HandleAnswer(response.SDP + candidates); err != nil {
					t.Fatal(err)
				}
			} else {
				offer, err := client.CreateOffer(nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = peer.HandleOffer(offer.SDP + candidates); err != nil {
					t.Fatal(err)
				}
			}
			for _, candidate := range []string{"candidate:15 1 udp 2130706427 ::ffff:127.0.0.1 40005 typ host", "candidate:16 1 udp 2130706426 198.51.100.11 40006 typ relay raddr 0.0.0.0 rport 0", ""} {
				if err = peer.AddICECandidate(pion.ICECandidateInit{Candidate: candidate}); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				seen := map[string]bool{}
				for _, stat := range peer.pc.GetStats() {
					if c, ok := stat.(pion.ICECandidateStats); ok && c.Type == pion.StatsTypeRemoteCandidate {
						if !strings.HasPrefix(c.IP, "198.51.100.") {
							t.Fatalf("denied candidate admitted: %+v", c)
						}
						seen[c.IP] = true
					}
				}
				if seen["198.51.100.10"] && seen["198.51.100.11"] {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("public control missing: %v", seen)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
