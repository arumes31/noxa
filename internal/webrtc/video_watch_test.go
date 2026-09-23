package webrtc

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func testVideoPublication(t *testing.T, r *Router, publisher, subscriber, slot string) uint64 {
	t.Helper()
	r.watchMu.RLock()
	existing := r.publications[publicationKey{publisher, slot}]
	r.watchMu.RUnlock()
	generation, err := r.PublishVideo(publisher, slot, existing, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WatchVideo(subscriber, publisher, slot, generation, 1, r.VideoWatchSession(subscriber), true); err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestVideoWatchRejectsEarlierReceiverSession(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	session := r.VideoWatchSession("sub")
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 2, session, false); err != nil {
		t.Fatal(err)
	}
	r.SetPublisherGuard(func(PublisherAccess) bool { return false })
	r.SetPublisherGuard(nil)
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 1, session, true); err == nil {
		t.Fatal("pruning lost watch high-water mark")
	}
	r.DetachPeerKeepChannel("sub")
	newSession := r.VideoWatchSession("sub")
	if newSession == session {
		t.Fatal("receiver session reused")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 99, session, true); err == nil {
		t.Fatal("old receiver request reactivated video")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 1, newSession, true); err != nil {
		t.Fatal(err)
	}
}

func TestVideoWatchReportsOnlyNewViewerStarts(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("sub")
	for _, tc := range []struct {
		revision                  uint64
		active, started, rejected bool
	}{
		{1, true, true, false}, {1, true, false, false}, {2, true, false, false},
		{3, false, false, false}, {2, true, false, true}, {4, true, true, false},
	} {
		started, err := r.WatchVideo("sub", "pub", SlotScreen, generation, tc.revision, session, tc.active)
		if started != tc.started || (err != nil) != tc.rejected {
			t.Fatalf("%+v: started=%v err=%v", tc, started, err)
		}
	}
	r.JoinChannel(2, "sub")
	if started, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 5, session, true); started || err == nil {
		t.Fatal("unauthorized viewer start")
	}
}

func TestVideoWatchStopDrainsFinalWrite(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	session := r.VideoWatchSession("sub")
	r.mu.RLock()
	d := r.mediaOutputLocked("pub", "sub", SlotScreen, false, nil).delivery
	r.mu.RUnlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = r.mediaCommit(d, nil, 0)(func() error { close(entered); <-release; return nil })
		close(done)
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		_, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 2, session, false)
		stopped <- err
	}()
	select {
	case err := <-stopped:
		close(release)
		<-done
		t.Fatalf("stop returned before write drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestVideoPreviewScopeRateAndLifecycle(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(2, "outside")
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 320, 180)), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.SetVideoPreview("pub", SlotScreen, generation, jpegData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.VideoPreview("outside", "pub", SlotScreen, generation); err == nil {
		t.Fatal("preview escaped channel")
	}
	data, captured, err := r.VideoPreview("sub", "pub", SlotScreen, generation)
	if err != nil || captured <= 0 || !bytes.Equal(data, jpegData.Bytes()) {
		t.Fatal("unwatched member cannot see preview")
	}
	if err := r.SetVideoPreview("pub", SlotScreen, generation, jpegData.Bytes()); err == nil {
		t.Fatal("preview rate not bounded")
	}
	r.RevokeVideo("pub", SlotScreen)
	if _, _, err := r.VideoPreview("sub", "pub", SlotScreen, generation); err == nil {
		t.Fatal("revoked preview available")
	}
	next, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetVideoPreview("pub", SlotScreen, next, jpegData.Bytes()); err == nil {
		t.Fatal("republishing bypassed preview budget")
	}
}

func TestVideoWatchDefaultOffAndGeneration(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	capture := func(slot string, tap bool) MediaDelivery {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.mediaOutputLocked("pub", "sub", slot, tap, nil).delivery
	}
	allowed := func(d MediaDelivery) bool {
		wrote := false
		_ = r.mediaCommit(d, nil, 0)(func() error { wrote = true; return nil })
		return wrote
	}
	if allowed(capture(SlotCam, false)) || allowed(capture(SlotScreenAudio, false)) {
		t.Fatal("video defaulted to on")
	}
	if !allowed(capture(SlotMic, false)) || !allowed(capture(SlotCam, true)) {
		t.Fatal("watch gate affected voice or recording")
	}
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	old := capture(SlotScreen, false)
	if !allowed(old) || !allowed(capture(SlotScreenAudio, false)) {
		t.Fatal("explicit watch did not enable video and shared audio")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 2, r.VideoWatchSession("sub"), false); err != nil {
		t.Fatal(err)
	}
	if allowed(old) || allowed(capture(SlotScreenAudio, false)) {
		t.Fatal("stop kept delivering")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true); err == nil {
		t.Fatal("stale watch accepted")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 3, r.VideoWatchSession("sub"), true); err != nil {
		t.Fatal(err)
	}
	if allowed(old) || !allowed(capture(SlotScreen, false)) {
		t.Fatal("resume reused old packet authority")
	}
	r.DetachPeerKeepChannel("sub")
	if allowed(capture(SlotScreen, false)) {
		t.Fatal("peer rebuild retained watch")
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	next, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil || next == generation {
		t.Fatal("publication reused generation")
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err == nil {
		t.Fatal("stale stop ended new publication")
	}
}

func TestVideoWatchQueuedRetransmission(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	r.mu.RLock()
	delivery := r.mediaOutputLocked("pub", "sub", SlotScreenAudio, false, nil).delivery
	r.mu.RUnlock()
	stream := &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}
	packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{SSRC: 42}, Payload: []byte{1}}, mediaTicket{router: r, delivery: delivery})
	if !ok {
		t.Fatal("prepare failed")
	}
	writes := 0
	writer := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { writes++; return 1, nil })
	r.videoPolicyMu.Lock()
	r.videoPolicy.revision++
	r.videoPolicyMu.Unlock()
	if _, err := stream.write(&packet.Header, packet.Payload, nil, writer); err != nil || writes != 1 {
		t.Fatal("video policy revision blocked watched screen audio")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 2, r.VideoWatchSession("sub"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.write(&packet.Header, packet.Payload, nil, writer); err != nil || writes != 1 {
		t.Fatal("stopped packet retransmitted")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 3, r.VideoWatchSession("sub"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.write(&packet.Header, packet.Payload, nil, writer); err != nil || writes != 1 {
		t.Fatal("old packet retransmitted after resume")
	}
}
