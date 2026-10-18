package webrtc

import (
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func dispatchPacket(sequence uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, SSRC: 9, CSRC: []uint32{7}}, Payload: []byte{1, 2, 3}}
}

func TestVideoDispatchReaderProgressOrderAndOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var packets []*rtp.Packet
		var formats []pion.RTPCodecCapability
		var gaps []bool
		d := newVideoIngressDispatch(func() bool { return true }, func(p *rtp.Packet, c pion.RTPCodecCapability, _ time.Time, gap bool) {
			if p.SequenceNumber == 1 {
				close(entered)
				<-release
			}
			packets = append(packets, p)
			formats = append(formats, c)
			gaps = append(gaps, gap)
		})
		codec := pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, RTCPFeedback: []pion.RTCPFeedback{{Type: "nack"}}}
		d.enqueue(dispatchPacket(1), codec)
		<-entered
		source := dispatchPacket(2)
		if !d.enqueue(source, codec) || !d.enqueue(dispatchPacket(3), codec) {
			t.Fatal("reader admission blocked/dropped behind consumer")
		}
		source.Payload[0], source.CSRC[0], source.SequenceNumber = 99, 99, 99
		codec.RTCPFeedback[0].Type = "changed"
		d.mu.Lock()
		queued := d.count
		d.mu.Unlock()
		if queued != 2 {
			t.Fatalf("pending count=%d", queued)
		}
		close(release)
		d.finish(true)
		if len(packets) != 3 || packets[1].SequenceNumber != 2 || packets[2].SequenceNumber != 3 || packets[1].Payload[0] != 1 || packets[1].CSRC[0] != 7 || formats[1].RTCPFeedback[0].Type != "nack" || !reflect.DeepEqual(gaps, []bool{false, false, false}) {
			t.Fatal("FIFO input ownership or format identity changed")
		}
	})
}

func TestVideoDispatchOverflowMarksOnlyLaterAdmittedPackets(t *testing.T) {
	for _, mode := range []string{"packets", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, release, consumed := make(chan struct{}), make(chan struct{}), make(chan struct{})
				limits := videoDispatchLimits{2, 65535, time.Second}
				if mode == "bytes" {
					limits = videoDispatchLimits{10, dispatchPacket(1).MarshalSize() * 2, time.Second}
				}
				var sequences []uint16
				var gaps []bool
				d := newVideoIngressDispatchWithLimits(func() bool { return true }, func(p *rtp.Packet, _ pion.RTPCodecCapability, _ time.Time, gap bool) {
					if p.SequenceNumber == 1 {
						close(entered)
						<-release
					}
					sequences = append(sequences, p.SequenceNumber)
					gaps = append(gaps, gap)
					if p.SequenceNumber == 3 {
						close(consumed)
					}
				}, time.Now, limits)
				codec := pion.RTPCodecCapability{}
				d.enqueue(dispatchPacket(1), codec)
				<-entered
				if !d.enqueue(dispatchPacket(2), codec) || !d.enqueue(dispatchPacket(3), codec) || d.enqueue(dispatchPacket(4), codec) {
					t.Fatal("capacity not enforced")
				}
				d.mu.Lock()
				count, bytes, drops := d.count, d.bytes, d.dropped
				d.mu.Unlock()
				if count != 2 || bytes > limits.bytes || drops != 1 {
					t.Fatal("retained accounting exceeded limits")
				}
				close(release)
				<-consumed
				d.enqueue(dispatchPacket(5), codec)
				d.finish(true)
				if !reflect.DeepEqual(sequences, []uint16{1, 2, 3, 5}) || !reflect.DeepEqual(gaps, []bool{false, false, false, true}) {
					t.Fatalf("loss order: %v %v", sequences, gaps)
				}
			})
		})
	}
}

func TestVideoDispatchExpiryAndObservationTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release, fresh := make(chan struct{}), make(chan struct{}), make(chan struct{})
		start := time.Now()
		var sequences []uint16
		var gaps []bool
		var observed []time.Time
		d := newVideoIngressDispatch(func() bool { return true }, func(p *rtp.Packet, _ pion.RTPCodecCapability, at time.Time, gap bool) {
			if p.SequenceNumber == 1 {
				close(entered)
				<-release
			}
			sequences = append(sequences, p.SequenceNumber)
			gaps = append(gaps, gap)
			observed = append(observed, at)
			if p.SequenceNumber == 3 {
				close(fresh)
			}
		})
		d.enqueue(dispatchPacket(1), pion.RTPCodecCapability{})
		<-entered
		d.enqueue(dispatchPacket(2), pion.RTPCodecCapability{})
		time.Sleep(videoDispatchLifetime + time.Millisecond)
		d.enqueue(dispatchPacket(3), pion.RTPCodecCapability{})
		close(release)
		<-fresh
		d.finish(true)
		if !reflect.DeepEqual(sequences, []uint16{1, 3}) || !reflect.DeepEqual(gaps, []bool{false, true}) || observed[0] != start || observed[1] != start.Add(videoDispatchLifetime+time.Millisecond) {
			t.Fatal("expired packet delivered or observation time rewritten")
		}
		if d.dropped != 1 {
			t.Fatal("expiry missing from accounting")
		}
	})
}

func TestVideoDispatchCancellationRetainsOwnedCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		d := newVideoIngressDispatch(func() bool { return true }, func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool) { calls.Add(1); close(entered); <-release })
		d.enqueue(dispatchPacket(1), pion.RTPCodecCapability{})
		<-entered
		d.enqueue(dispatchPacket(2), pion.RTPCodecCapability{})
		d.cancel()
		go func() { d.finish(false); close(finished) }()
		synctest.Wait()
		select {
		case <-finished:
			t.Fatal("detached active callback")
		default:
		}
		d.mu.Lock()
		empty := d.count == 0 && d.bytes == 0 && d.queue == nil
		d.mu.Unlock()
		if !empty || d.enqueue(dispatchPacket(3), pion.RTPCodecCapability{}) {
			t.Fatal("cancel retained/admitted queued work")
		}
		close(release)
		<-finished
		d.finish(false)
		if calls.Load() != 1 {
			t.Fatal("cancelled callback ran")
		}
	})
}

func TestVideoDispatchRetiredScopeDropsPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var current atomic.Bool
		current.Store(true)
		var calls int
		d := newVideoIngressDispatch(current.Load, func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool) { calls++; close(entered); <-release })
		d.enqueue(dispatchPacket(1), pion.RTPCodecCapability{})
		<-entered
		d.enqueue(dispatchPacket(2), pion.RTPCodecCapability{})
		current.Store(false)
		close(release)
		d.finish(true)
		if calls != 1 || !d.cancelled.Load() {
			t.Fatal("retired source emitted queued work")
		}
	})
}

// The real router must keep reading while fanout is held, and join that
// fanout before retiring its source. Virtual time makes expiry deterministic.
type dispatchRouterTrack struct {
	diagnosticVideoTrack
	mime string
	id   string
}

func (t *dispatchRouterTrack) Codec() pion.RTPCodecParameters {
	fmtp := ""
	if t.mime == pion.MimeTypeH264 {
		fmtp = "packetization-mode=1;profile-level-id=42001f"
	}
	if t.mime == pion.MimeTypeH265 {
		fmtp = hevcRouterFmtp
	}
	return pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: t.mime, ClockRate: 90000, SDPFmtpLine: fmtp}}
}

type dispatchHeldWriter struct {
	entered, release chan struct{}
	packets          []*rtp.Packet
}

func (w *dispatchHeldWriter) WriteRTP(p *rtp.Packet) error {
	if len(w.packets) == 0 {
		close(w.entered)
		<-w.release
	}
	w.packets = append(w.packets, p.Clone())
	return nil
}
func TestVideoDispatchRouterReadProgressExpiryAndJoin(t *testing.T) {
	for _, mime := range []string{pion.MimeTypeVP8, pion.MimeTypeH264, pion.MimeTypeH265} {
		t.Run(mime, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				key := func(seq uint16) *rtp.Packet {
					switch mime {
					case pion.MimeTypeH264:
						return h264ForwardPacket(seq, uint32(seq)*90, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1)
					case pion.MimeTypeH265:
						return hevcPacket(seq, uint32(seq)*90, true, hevcKey(19)...)
					default:
						return boundsKeyPacket(seq, uint32(seq)*90, 1280, 720)
					}
				}
				delta := func(seq uint16) *rtp.Packet {
					switch mime {
					case pion.MimeTypeH264:
						return h264ForwardPacket(seq, uint32(seq)*90, true, 0x41, 1)
					case pion.MimeTypeH265:
						return hevcPacket(seq, uint32(seq)*90, true, hevcNAL(1, true)...)
					default:
						return boundsDeltaPacket(seq, uint32(seq)*90)
					}
				}
				r := NewRouter(nil)
				r.JoinChannel(1, "publisher")
				r.JoinChannel(1, "recorder")
				if mime == pion.MimeTypeH265 {
					if _, err := r.PublishVideoWithFormats("publisher", SlotCam, 0, true, "source", []string{"h265", "h264", "vp8"}, 93); err != nil {
						t.Fatal(err)
					}
				}
				w := &dispatchHeldWriter{entered: make(chan struct{}), release: make(chan struct{})}
				r.addVideoOutput("recorder", w)
				tr := &dispatchRouterTrack{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 7}, packets: make(chan *rtp.Packet)}, mime: mime}
				done := make(chan struct{})
				go func() { r.ReadVideoLoop("publisher", SlotCam, tr); close(done) }()
				tr.packets <- key(1)
				<-w.entered
				// This send plus the following read barrier must finish while the
				// writer is still held. The old synchronous path deadlocks here.
				tr.packets <- delta(2)
				tr.packets <- nil
				synctest.Wait()
				time.Sleep(videoDispatchLifetime + time.Millisecond)
				tr.packets <- delta(3)
				tr.packets <- key(4)
				tr.packets <- delta(5)
				tr.packets <- nil
				close(tr.packets)
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("reader detached its active writer")
				default:
				}
				close(w.release)
				<-done
				var seq []uint16
				for _, p := range w.packets {
					seq = append(seq, p.SequenceNumber)
				}
				if !reflect.DeepEqual(seq, []uint16{1, 4, 5}) {
					t.Fatalf("expiry leaked dependent frames or lost recovery: %v", seq)
				}
				r.mu.RLock()
				sources := len(r.videoSources["publisher"])
				r.mu.RUnlock()
				if sources != 0 {
					t.Fatal("source remained after joined shutdown")
				}
			})
		})
	}
}

// Tests that change control policy between media units must wait for dispatch,
// not merely the next read. This barrier makes the original scenario explicit;
// production reading remains asynchronous, as the held-writer tests prove.
func waitVideoDispatchIdle(t *testing.T, r *Router) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		idle := true
		r.mu.RLock()
		for _, input := range r.videoIngress {
			if d := input.dispatch.Load(); d != nil {
				s := d.snapshot(time.Now())
				if s.QueuedPackets != 0 || s.CallbackActive {
					idle = false
				}
			}
		}
		r.mu.RUnlock()
		if idle {
			waitAsyncKeyframeRequests(t, r)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("dispatch did not finish prior test media")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestVideoDispatchOlderSlotTimestampCannotRefillSharedBudget(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	if err := r.SetVideoBitrateLimit(8000); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if !r.allowVideoPacket("publisher", 1000, now) || !r.allowVideoPacket("publisher", 1000, now.Add(time.Second)) {
		t.Fatal("fixture budget")
	}
	if r.allowVideoPacket("publisher", 1, now.Add(500*time.Millisecond)) || r.allowVideoPacket("publisher", 1, now.Add(time.Second)) {
		t.Fatal("older queued slot rewound/refilled shared budget")
	}
	if !r.allowVideoPacket("publisher", 500, now.Add(1500*time.Millisecond)) {
		t.Fatal("normal forward refill broken")
	}
}
func TestVideoDispatchSnapshotAccountsForExpiryAndActiveCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		d := newVideoIngressDispatchWithLimits(func() bool { return true }, func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool) { close(entered); <-release }, time.Now, videoDispatchLimits{1, 65535, time.Millisecond})
		d.enqueue(dispatchPacket(1), pion.RTPCodecCapability{})
		<-entered
		d.enqueue(dispatchPacket(2), pion.RTPCodecCapability{})
		d.enqueue(dispatchPacket(3), pion.RTPCodecCapability{})
		time.Sleep(2 * time.Millisecond)
		s := d.snapshot(time.Now())
		if s.QueuedPackets != 1 || s.QueuedBytes != dispatchPacket(2).MarshalSize() || s.OldestQueuedMS != 2 || s.OverflowDrops != 1 || !s.CallbackActive || s.CallbackAgeMS != 2 {
			t.Fatalf("live counters: %+v", s)
		}
		close(release)
		d.finish(true)
		ended := d.snapshot(time.Now())
		if ended.QueuedPackets != 0 || ended.ExpiryDrops != 1 || ended.MaxWaitMS != 2 || ended.MaxCallbackMS != 2 || ended.CallbackActive {
			t.Fatalf("ended counters: %+v", ended)
		}
		if s.QueuedPackets != 1 || s.ExpiryDrops != 0 {
			t.Fatal("snapshot aliased mutable counters")
		}
	})
}

func (t *dispatchRouterTrack) ID() string { return t.id }

func TestVideoDispatchGuardHeldSameSSRCReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRouter(nil)
		r.JoinChannel(1, "publisher")
		r.JoinChannel(1, "recorder")
		r.SetTrackSlots("publisher", map[string]string{"old": SlotCam})
		out := &fakeTrackWriter{}
		r.addVideoOutput("recorder", out)
		entered, release := make(chan struct{}), make(chan struct{})
		var captured MediaDelivery
		r.SetMediaGuard(func(d MediaDelivery, write func() error) error {
			captured = d
			close(entered)
			<-release
			return write()
		})
		tr := &dispatchRouterTrack{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 7}, packets: make(chan *rtp.Packet)}, mime: pion.MimeTypeVP8, id: "old"}
		done := make(chan struct{})
		go func() { r.ReadVideoLoop("publisher", SlotCam, tr); close(done) }()
		tr.packets <- boundsKeyPacket(1, 90, 1280, 720)
		<-entered
		tr.packets <- boundsDeltaPacket(2, 180)
		tr.packets <- nil
		oldEpoch := captured.SourceEpoch
		if oldEpoch == 0 || captured.sourceLive == nil || !captured.sourceLive.Load() {
			t.Fatal("delivery lost immutable source authority")
		}
		r.SetTrackSlots("publisher", map[string]string{"new": SlotCam})
		next, ok := r.claimSlot("publisher", SlotCam+slotSep+"f", "new")
		if !ok || next == oldEpoch {
			t.Fatal("replacement fixture did not create new epoch")
		}
		// The source SSRC deliberately remains unchanged; token/liveness must win.
		if captured.sourceLive.Load() {
			t.Fatal("old source remained live")
		}
		close(tr.packets)
		close(release)
		<-done
		if out.count() != 0 || captured.SourceEpoch != oldEpoch || !r.ownsSlot("publisher", SlotCam+slotSep+"f", next) {
			t.Fatal("old packet rebound to replacement or old cleanup removed it")
		}
		r.releaseSlot("publisher", SlotCam+slotSep+"f", next)
	})
}

func TestVideoDispatchQueuedWatchAndPolicyRevocation(t *testing.T) {
	for _, mode := range []string{"watch", "policy", "permission"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewRouter(nil)
				r.JoinChannel(1, "pub")
				r.JoinChannel(1, "viewer")
				var allowed atomic.Bool
				allowed.Store(true)
				r.SetVideoHandlers(func(string) bool { return allowed.Load() })
				generation := testVideoPublication(t, r, "pub", "viewer", SlotCam)
				codec := pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}
				track, err := pion.NewTrackLocalStaticRTP(codec, "out", "out")
				if err != nil {
					t.Fatal(err)
				}
				capture := &hevcRouterCapture{}
				if _, err := track.Bind(hevcRouterTrackContext{extensionTrackContext: extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: codec, PayloadType: 96}}, capture: capture}); err != nil {
					t.Fatal(err)
				}
				output := &pubSlot{track: track}
				r.pubTracks["viewer"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotCam: output}}}
				entered, release := make(chan struct{}), make(chan struct{})
				var first atomic.Bool
				r.SetMediaGuard(func(_ MediaDelivery, write func() error) error {
					if first.CompareAndSwap(false, true) {
						close(entered)
						<-release
					}
					return write()
				})
				tr := &dispatchRouterTrack{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 7}, packets: make(chan *rtp.Packet)}, mime: pion.MimeTypeVP8}
				done := make(chan struct{})
				go func() { r.ReadVideoLoop("pub", SlotCam, tr); close(done) }()
				tr.packets <- boundsKeyPacket(1, 90, 1280, 720)
				<-entered
				tr.packets <- boundsDeltaPacket(2, 180)
				tr.packets <- nil
				switch mode {
				case "watch":
					if _, err := r.WatchVideo("viewer", "pub", SlotCam, generation, 2, r.VideoWatchSession("viewer"), false); err != nil {
						t.Fatal(err)
					}
				case "policy":
					if err := r.SetVideoLimits(0, VideoBounds{640, 360}); err != nil {
						t.Fatal(err)
					}
				case "permission":
					allowed.Store(false)
				}
				close(tr.packets)
				close(release)
				<-done
				want := 0
				// Permission callbacks are admission checks: the first callback entered
				// before revocation; the pending packet must still be rechecked and dropped.
				if mode == "permission" {
					want = 1
				}
				if len(capture.packets) != want {
					t.Fatalf("%s forwarded %d want %d", mode, len(capture.packets), want)
				}
			})
		})
	}
}
