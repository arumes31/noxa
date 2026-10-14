package webrtc

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
)

// Registered last, this holds Send after production's pacer bind notification,
// but before Pion publishes interceptorToTrackLocalWriter.interceptor.
type heldStartupBind struct {
	interceptor.NoOp
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	enabled atomic.Bool
}

func (h *heldStartupBind) NewInterceptor(string) (interceptor.Interceptor, error) { return h, nil }
func (h *heldStartupBind) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if h.enabled.Load() && !strings.EqualFold(info.MimeType, pion.MimeTypeOpus) {
		h.once.Do(func() { close(h.entered); <-h.release })
	}
	return writer
}

type startupRTCPFunc func([]rtcp.Packet) error

func (f startupRTCPFunc) WriteRTCP(packets []rtcp.Packet) error { return f(packets) }

func TestWatchStartupBindingWaitsForPublishedPionWriter(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	held := &heldStartupBind{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	t.Cleanup(func() {
		release()
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	media, registry, err := newEngineMediaObserved(false, VideoBounds{}, engine.egress, engine.upstreamFeedback)
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(held)
	engine.api = pion.NewAPI(pion.WithMediaEngine(media), pion.WithSettingEngine(engine.settingEngine), pion.WithInterceptorRegistry(registry))
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
	t.Cleanup(release) // Release a held Send before receiver/engine teardown on failure.
	negotiateStartupTestPeer(t, peer, receiver)
	deadline := time.Now().Add(5 * time.Second)
	for peer.pc.ConnectionState() != pion.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if peer.pc.ConnectionState() != pion.PeerConnectionStateConnected {
		t.Fatal("existing transport not connected")
	}
	held.enabled.Store(true)
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
	negotiated := make(chan error, 1)
	go func() { negotiated <- negotiateStartupTestPeerError(peer, receiver) }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("outer binding was not reached")
	}
	// Give the notified worker time to run while Send deliberately holds its lock.
	time.Sleep(50 * time.Millisecond)
	if !output.videoPacerBound() || output.videoOutputBound() || writer.count(42) != 0 {
		t.Fatalf("partial bind admitted startup: pacer=%v ready=%v PLI=%d", output.videoPacerBound(), output.videoOutputBound(), writer.count(42))
	}
	before, epoch := output.videoContinuity, output.watchEpoch
	if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 1, 3000, 1, true)); n != 0 {
		t.Fatalf("partial binding admitted %d packets", n)
	}
	if output.videoContinuity != before || output.watchEpoch != epoch || output.videoSource.Load() != 0 {
		t.Fatal("partial binding consumed unseen keyframe continuity")
	}
	// The binding barrier must not own Router/watch locks while waiting for Send.
	unlocked := make(chan struct{})
	go func() {
		r.mu.Lock()
		r.watchMu.Lock()
		close(unlocked) // Signal only after acquiring both locks while Send is held.
		r.watchMu.Unlock()
		r.mu.Unlock()
	}()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		t.Fatal("binding wait held Router/watch locks")
	}
	release()
	select {
	case err := <-negotiated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("negotiation did not finish")
	}
	deadline = time.Now().Add(5 * time.Second)
	for writer.count(42) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if writer.count(42) != 1 || !output.videoTransportReady(r, "viewer") {
		t.Fatalf("ready static source not requested once: PLI=%d", writer.count(42))
	}
	if n := r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 2, 6000, 2, true)); n != 1 {
		t.Fatalf("published binding admitted %d packets", n)
	}
}

func TestWatchStartupCloseDoesNotJoinForeignPublisherWrite(t *testing.T) {
	r, peer, _, generation, session := directStartupFixture(t)
	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	peer.pc = pc
	peer.logger = testLogger()
	peer.clientID = "viewer"
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		if err := pc.Close(); err != nil {
			t.Error(err)
		}
	})
	r.mu.Lock()
	r.rtcpWriters["pub"] = startupRTCPFunc(func([]rtcp.Packet) error { close(entered); <-release; close(finished); return nil })
	r.mu.Unlock()
	// First record intent without a recipient; readiness later starts the worker.
	if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.pubPeers["viewer"] = peer
	r.mu.Unlock()
	peer.notifyVideoStartup()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("foreign write not entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- peer.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		unblock()
		t.Fatal("recipient Close waited for foreign publisher write")
	}
	select {
	case <-finished:
		t.Fatal("foreign writer unexpectedly released")
	default:
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("foreign writer did not finish after release")
	}
	peer.videoStartupMu.Lock()
	done := peer.videoStartupDone
	peer.videoStartupMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed recipient worker did not retire")
	}
}
