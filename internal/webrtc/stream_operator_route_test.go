package webrtc

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func routeFixture() (*Router, StreamRouteRequest, selectedStreamRoute) {
	r := NewRouter(nil)
	r.clientChan["pub"] = 7
	r.pubPeers["pub"] = &PeerConnectionWrapper{}
	r.publications = map[publicationKey]uint64{{"pub", SlotScreen}: 23}
	r.videoSources["pub"] = map[string]map[string]uint32{SlotScreen: {"": 42}}
	r.videoIngress = map[videoIngressKey]*videoIngressDiagnostic{{"pub", SlotScreen, ""}: {ssrc: 42, started: time.Now()}}
	return r, StreamRouteRequest{PublisherID: "pub", Slot: SlotScreen, Generation: 23, SSRC: 42}, selectedStreamRoute{peerID: "peer-one", localUfrag: "private-local-ufrag", remoteUfrag: "private-remote-ufrag", local: StreamRouteCandidate{Address: "192.0.2.1", Port: 12334, Protocol: "udp", Type: "host"}, remote: StreamRouteCandidate{Address: "198.51.100.2", Port: 50000, Protocol: "udp", Type: "prflx"}}
}
func TestStreamRouteScopedSnapshotAndEpoch(t *testing.T) {
	r, q, route := routeFixture()
	read := func(*PeerConnectionWrapper) (selectedStreamRoute, error) { return route, nil }
	got, err := r.streamRoute(q, read)
	if err != nil || got.Generation != 23 || got.SSRC != 42 || got.PeerID != "peer-one" || len(got.RouteToken) != 64 {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "password") || !strings.Contains(string(encoded), `"generation":"23"`) {
		t.Fatalf("secret or epoch encoding: %s", encoded)
	}
	q.RouteToken = got.RouteToken
	if _, err = r.streamRoute(q, read); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*selectedStreamRoute){func(s *selectedStreamRoute) { s.peerID = "peer-two" }, func(s *selectedStreamRoute) { s.localUfrag = "new-local" }, func(s *selectedStreamRoute) { s.remoteUfrag = "new-remote" }, func(s *selectedStreamRoute) { s.remote.Port++ }} {
		changed := route
		change(&changed)
		if _, err = r.streamRoute(q, func(*PeerConnectionWrapper) (selectedStreamRoute, error) { return changed, nil }); err == nil {
			t.Fatal("stale route token admitted")
		}
	}
}
func TestStreamRouteRejectsScopeChangesDuringRead(t *testing.T) {
	for _, name := range []string{"publication", "peer", "channel", "ingress", "source", "pair"} {
		t.Run(name, func(t *testing.T) {
			r, q, route := routeFixture()
			reads := 0
			_, err := r.streamRoute(q, func(*PeerConnectionWrapper) (selectedStreamRoute, error) {
				reads++
				if reads == 1 {
					switch name {
					case "publication":
						r.publications[publicationKey{"pub", SlotScreen}]++
					case "peer":
						r.pubPeers["pub"] = &PeerConnectionWrapper{}
					case "channel":
						r.clientChan["pub"]++
					case "ingress":
						r.videoIngress[videoIngressKey{"pub", SlotScreen, ""}] = &videoIngressDiagnostic{ssrc: 42}
					case "source":
						r.videoSources["pub"][SlotScreen][""] = 43
					}
				}
				if name == "pair" && reads == 2 {
					route.remote.Port++
				}
				return route, nil
			})
			if err == nil {
				t.Fatal("changed scope admitted")
			}
		})
	}
	r, q, route := routeFixture()
	q.Generation++
	calls := 0
	if _, err := r.streamRoute(q, func(*PeerConnectionWrapper) (selectedStreamRoute, error) { calls++; return route, nil }); err == nil || calls != 0 {
		t.Fatal("stale publication queried ICE")
	}
}
func TestStreamRouteCandidateAllowlist(t *testing.T) {
	candidate := pion.ICECandidate{Address: "192.0.2.1", Port: 12334, Protocol: pion.ICEProtocolUDP, Typ: pion.ICECandidateTypeHost, Component: 1, Foundation: "private-foundation", RelatedAddress: "private-related"}
	got, ok := streamRouteCandidate(&candidate)
	if !ok || got.Address != "192.0.2.1" || got.Port != 12334 || got.Protocol != "udp" {
		t.Fatalf("candidate %+v", got)
	}
	for _, address := range []string{"example.local", "0.0.0.0", "224.0.0.1", ""} {
		candidate.Address = address
		if _, ok = streamRouteCandidate(&candidate); ok {
			t.Fatal("unusable address accepted")
		}
	}
	candidate.Address = "2001:db8::1"
	candidate.Protocol = pion.ICEProtocolTCP
	if got, ok = streamRouteCandidate(&candidate); !ok || got.Protocol != "tcp" {
		t.Fatal("numeric IPv6 TCP rejected")
	}
	candidate.Port = 0
	if _, ok = streamRouteCandidate(&candidate); ok {
		t.Fatal("zero port")
	}
	if _, err := readSelectedStreamRoute(nil); err == nil {
		t.Fatal("absent peer admitted")
	}
}

// Exercise the actual Pion ICE getter through a media-only connection: the
// operator endpoint must not require a negotiated data channel to locate ICE.
func TestStreamRoutePionMediaOnlyGetter(t *testing.T) {
	setting := pion.SettingEngine{}
	setting.SetIncludeLoopbackCandidate(true)
	setting.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	setting.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	api := pion.NewAPI(pion.WithSettingEngine(setting))
	first, err := api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})
	second, err := api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = first.AddTransceiverFromKind(pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{}, 1)
	first.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	gather := func(pc *pion.PeerConnection, desc pion.SessionDescription) {
		t.Helper()
		done := pion.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(desc); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("local ICE gathering timed out")
		}
	}
	offer, err := first.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(first, offer)
	if err = second.SetRemoteDescription(*first.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := second.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(second, answer)
	if err = first.SetRemoteDescription(*second.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("local ICE connection timed out")
	}
	peer := &PeerConnectionWrapper{pc: first}
	route, err := readSelectedStreamRoute(peer)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []StreamRouteCandidate{route.local, route.remote} {
		if ip := net.ParseIP(candidate.Address); ip == nil || !ip.IsLoopback() || candidate.Port == 0 || candidate.Protocol != "udp" {
			t.Fatalf("unexpected local candidate: %+v", candidate)
		}
	}
	if route.peerID != first.ID() || route.localUfrag == "" || route.remoteUfrag == "" {
		t.Fatal("missing Pion connection identity")
	}
	again, err := readSelectedStreamRoute(peer)
	if err != nil || route != again {
		t.Fatalf("unchanged pair did not remain stable: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = readSelectedStreamRoute(peer); err == nil {
		t.Fatal("closed connection retained an admissible route")
	}
}
