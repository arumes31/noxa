package webrtc

import (
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Leave the production connection callback installed. Readiness must request a
// frame even if a static source never sends again after an undeliverable frame.
func TestWatchStartWaitsForBindingAndTransportWithoutMoreSourcePackets(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	engine.iceServers = nil
	peer, err := engine.NewPeerConnection("viewer")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	if err = r.AttachPeer("viewer", peer); err != nil {
		t.Fatal(err)
	}
	r.PrepareSubscriber("viewer")
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	registerVideoSource(r, "pub", SlotScreen, "f", 42)
	writer := &startupPLIWriter{counts: make(map[uint32]int)}
	r.mu.Lock()
	r.rtcpWriters["pub"] = writer
	r.mu.Unlock()
	session := r.VideoWatchSession("viewer")
	if started, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil || !started || writer.count(42) != 0 {
		t.Fatalf("unready watch: started=%v err=%v PLI=%d", started, err, writer.count(42))
	}
	output := pubTrackFor(r, "viewer", "pub").video[SlotScreen]
	if output.videoTransportReady(r, "viewer") {
		t.Fatal("unnegotiated output ready")
	}
	before, epoch := output.videoContinuity, output.watchEpoch
	if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 1, 3000, 1, true)); n != 0 {
		t.Fatalf("unready output forwarded %d", n)
	}
	if output.videoContinuity != before || output.watchEpoch != epoch || output.videoSource.Load() != 0 {
		t.Fatal("unreceived keyframe committed continuity")
	}
	if started, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil || started {
		t.Fatalf("duplicate: %v %v", started, err)
	}
	// Existing consumer-start hooks must not bypass the pending readiness gate.
	r.JoinChannel(1, "viewer")
	r.EnsurePublishers("viewer")
	if writer.count(42) != 0 {
		t.Fatal("consumer-start bypassed readiness")
	}

	receiver := newStartupTestReceiver(t)
	negotiateStartupTestPeer(t, peer, receiver)
	deadline := time.Now().Add(5 * time.Second)
	for writer.count(42) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if writer.count(42) != 1 || !output.videoTransportReady(r, "viewer") {
		t.Fatalf("static readiness PLI=%d ready=%v", writer.count(42), output.videoTransportReady(r, "viewer"))
	}
	if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 2, 6000, 2, false)); n != 0 || writer.count(42) != 1 {
		t.Fatal("ready delta bypassed keyframe or shared throttle")
	}
	if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 3, 9000, 3, true)); n != 1 {
		t.Fatalf("deliverable keyframe outputs=%d", n)
	}
	if !output.videoContinuity.seen || output.videoSource.Load() != 42 || writer.count(42) != 1 {
		t.Fatal("ready keyframe did not establish continuity with one startup request")
	}
}

// Explicit direct-output peer for deterministic policy tests. The managed
// output binding and real ICE/DTLS event boundary are covered above.
func directStartupFixture(t *testing.T) (*Router, *PeerConnectionWrapper, *startupPLIWriter, uint64, uint64) {
	t.Helper()
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	registerVideoSource(r, "pub", SlotScreen, "f", 42)
	writer := &startupPLIWriter{counts: make(map[uint32]int)}
	r.mu.Lock()
	r.rtcpWriters["pub"] = writer
	r.mu.Unlock()
	peer := &PeerConnectionWrapper{closed: make(chan struct{}), videoStartupWake: make(chan struct{}, 1)}
	peer.videoStartupHandler = func() time.Duration { return r.flushWatchStartup("viewer", peer) }
	t.Cleanup(func() {
		peer.closeOnce.Do(func() { close(peer.closed) })
		peer.videoStartupMu.Lock()
		done := peer.videoStartupDone
		peer.videoStartupMu.Unlock()
		if done != nil {
			<-done
		}
	})
	return r, peer, writer, generation, r.VideoWatchSession("viewer")
}

func TestWatchStartupSharedThrottleRetriesStaticSourceOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, peer, writer, generation, session := directStartupFixture(t)
		r.mu.Lock()
		r.pubPeers["viewer"] = peer
		r.mu.Unlock()
		r.RequestKeyframe("pub", SlotScreen, "f")
		time.Sleep(250 * time.Millisecond)
		if started, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil || !started {
			t.Fatalf("watch: %v %v", started, err)
		}
		synctest.Wait()
		time.Sleep(750*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		if writer.count(42) != 1 {
			t.Fatal("startup bypassed shared throttle")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if writer.count(42) != 2 {
			t.Fatal("static source not requested at shared throttle deadline")
		}
		for i := 0; i < 100; i++ {
			peer.notifyVideoStartup()
		}
		if started, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil || started {
			t.Fatalf("duplicate: %v %v", started, err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if writer.count(42) != 2 {
			t.Fatal("startup request repeated after completion")
		}
	})
}

func TestWatchStartupPendingCannotSurviveRevocation(t *testing.T) {
	for _, kind := range []string{"unwatch", "session", "publication", "channel", "guard", "peer", "close"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, peer, writer, generation, session := directStartupFixture(t)
				if _, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil {
					t.Fatal(err)
				}
				r.JoinChannel(1, "viewer")
				r.EnsurePublishers("viewer")
				if writer.count(42) != 0 {
					t.Fatal("watch requested before recipient existed")
				}
				r.mu.Lock()
				r.pubPeers["viewer"] = peer
				r.mu.Unlock()
				r.keyframeMu.Lock()
				r.keyframeLast[42] = time.Now()
				r.keyframeMu.Unlock()
				r.requestWatchStartup("viewer")
				synctest.Wait()
				switch kind {
				case "unwatch":
					if _, err := r.WatchVideo("viewer", "pub", SlotScreen, generation, 2, session, false); err != nil {
						t.Fatal(err)
					}
				case "session":
					r.DetachPeerKeepChannel("viewer")
				case "publication":
					r.RevokeVideo("pub", SlotScreen)
				case "channel":
					r.JoinChannel(2, "viewer")
				case "guard":
					r.SetPublisherGuard(func(PublisherAccess) bool { return false })
				case "peer":
					r.mu.Lock()
					r.pubPeers["viewer"] = &PeerConnectionWrapper{}
					r.mu.Unlock()
				case "close":
					peer.closeOnce.Do(func() { close(peer.closed) })
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if writer.count(42) != 0 {
					t.Fatalf("%s stale pending startup wrote PLI", kind)
				}
			})
		})
	}
}

func TestWatchStartupAdmittedFrameCancelsPendingThrottleRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, peer, writer, generation, session := directStartupFixture(t)
		track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, "screen", "pub")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = track.Bind(extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: track.Codec(), PayloadType: 96}, writer: &extensionCapture{}}); err != nil {
			t.Fatal(err)
		}
		output := &pubSlot{track: track}
		r.mu.Lock()
		r.pubTracks["viewer"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotScreen: output}}}
		r.pubPeers["viewer"] = peer
		r.mu.Unlock()
		r.RequestKeyframe("pub", SlotScreen, "f")
		if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 1, 3000, 1, true)); n != 1 {
			t.Fatalf("shared keyframe outputs=%d", n)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if writer.count(42) != 1 {
			t.Fatal("keyframe already admitted but pending retry still wrote")
		}
		// The old admitted watch epoch must not consume a later explicit retry.
		r.keyframeMu.Lock()
		r.keyframeLast[42] = time.Now()
		r.keyframeMu.Unlock()
		if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 2, session, true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if writer.count(42) != 2 {
			t.Fatal("stale admitted packet consumed newer watch startup")
		}
	})
}

func newStartupTestReceiver(t *testing.T) *pion.PeerConnection {
	t.Helper()
	setting := pion.SettingEngine{}
	setting.SetIncludeLoopbackCandidate(true)
	setting.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	setting.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	receiver, err := pion.NewAPI(pion.WithSettingEngine(setting)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := receiver.Close(); err != nil {
			t.Error(err)
		}
	})
	return receiver
}
func negotiateStartupTestPeer(t *testing.T, peer *PeerConnectionWrapper, receiver *pion.PeerConnection) {
	t.Helper()
	if err := negotiateStartupTestPeerError(peer, receiver); err != nil {
		t.Fatal(err)
	}
}

func negotiateStartupTestPeerError(peer *PeerConnectionWrapper, receiver *pion.PeerConnection) error {
	gather := func(pc *pion.PeerConnection, description pion.SessionDescription) error {
		done := pion.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(description); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("local gather timeout")
		}
	}
	offer, err := peer.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err = gather(peer.pc, offer); err != nil {
		return err
	}
	if err = receiver.SetRemoteDescription(*peer.pc.LocalDescription()); err != nil {
		return err
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err = gather(receiver, answer); err != nil {
		return err
	}
	return peer.pc.SetRemoteDescription(*receiver.LocalDescription())
}

func TestWatchStartupLateBindingOnConnectedTransport(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	engine.iceServers = nil
	peer, err := engine.NewPeerConnection("viewer")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	if err = r.AttachPeer("viewer", peer); err != nil {
		t.Fatal(err)
	}
	r.PrepareSubscriber("viewer")
	receiver := newStartupTestReceiver(t)
	negotiateStartupTestPeer(t, peer, receiver)
	deadline := time.Now().Add(5 * time.Second)
	for peer.pc.ConnectionState() != pion.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if peer.pc.ConnectionState() != pion.PeerConnectionStateConnected {
		t.Fatal("initial transport not connected")
	}
	// A new screen sender is still unbound although the existing transport is ready.
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	registerVideoSource(r, "pub", SlotScreen, "f", 42)
	writer := &startupPLIWriter{counts: make(map[uint32]int)}
	r.mu.Lock()
	r.rtcpWriters["pub"] = writer
	r.mu.Unlock()
	if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, r.VideoWatchSession("viewer"), true); err != nil {
		t.Fatal(err)
	}
	output := pubTrackFor(r, "viewer", "pub").video[SlotScreen]
	if writer.count(42) != 0 || output.videoTransportReady(r, "viewer") {
		t.Fatal("connected transport bypassed absent binding")
	}
	negotiateStartupTestPeer(t, peer, receiver)
	deadline = time.Now().Add(5 * time.Second)
	for writer.count(42) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if writer.count(42) != 1 || !output.videoTransportReady(r, "viewer") {
		t.Fatalf("late binding failed static source startup: PLI=%d ready=%v", writer.count(42), output.videoTransportReady(r, "viewer"))
	}
}
