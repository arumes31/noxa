package webrtc

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/goleak"
)

// verifyNoLeaks runs after every later-registered cleanup (engine, peers).
func verifyNoLeaks(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
}

func gopTestFrame(ssrc uint32, mime string, keyframe bool, sequence *uint16, frame uint32) []*rtp.Packet {
	packets := chaosFrame(mime, keyframe, 1, frame, sequence, frame*1500)
	for _, packet := range packets {
		packet.SSRC = ssrc
	}
	return packets
}

func TestGOPCacheBoundsAndInvalidation(t *testing.T) {
	now := time.Now()
	var sequence uint16 = 65530 // wraps inside the GOP
	var c gopCache
	key := gopTestFrame(5, pion.MimeTypeH264, true, &sequence, 1)
	c.observe(pion.MimeTypeH264, key, true, false, now)
	delta := gopTestFrame(5, pion.MimeTypeH264, false, &sequence, 2)
	c.observe(pion.MimeTypeH264, delta, false, false, now)
	next := gopTestFrame(5, pion.MimeTypeH264, false, &sequence, 3)
	if units := c.snapshotBefore(now, next[0], pion.MimeTypeH264); len(units) != 2 || !units[0].keyframe {
		t.Fatalf("cache did not hold keyframe + delta: %d", len(units))
	}
	if c.snapshotBefore(now, next[1], pion.MimeTypeH264) != nil {
		t.Fatal("cache served mid-unit, overlapping live media")
	}
	if c.snapshotBefore(now, next[0], pion.MimeTypeH265) != nil {
		t.Fatal("cache served a different codec")
	}
	if c.servable(now.Add(gopCacheLiveness+time.Millisecond), pion.MimeTypeH264) {
		t.Fatal("idle (possibly paused) source served a cached picture")
	}

	// A lost dependency after the keyframe invalidates until the next keyframe.
	c.observe(pion.MimeTypeH264, next, false, true, now)
	if c.servable(now, pion.MimeTypeH264) {
		t.Fatal("cache survived a lost unit")
	}
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, false, &sequence, 4), false, false, now)
	if c.servable(now, pion.MimeTypeH264) {
		t.Fatal("delta without keyframe made the cache servable")
	}

	// A sequence gap is a loss even without the assembler's flag.
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, true, &sequence, 5), true, false, now)
	sequence++
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, false, &sequence, 6), false, false, now)
	if c.servable(now, pion.MimeTypeH264) {
		t.Fatal("cache spanned a sequence gap")
	}

	// Codec switch (H.264 <-> H.265) and SSRC change invalidate.
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, true, &sequence, 7), true, false, now)
	c.observe(pion.MimeTypeH265, gopTestFrame(5, pion.MimeTypeH265, false, &sequence, 8), false, false, now)
	if units, _, _ := c.size(); units != 0 {
		t.Fatal("codec switch kept cached units")
	}
	c.observe(pion.MimeTypeH265, gopTestFrame(5, pion.MimeTypeH265, true, &sequence, 9), true, false, now)
	c.observe(pion.MimeTypeH265, gopTestFrame(6, pion.MimeTypeH265, false, &sequence, 10), false, false, now)
	if units, _, _ := c.size(); units != 0 {
		t.Fatal("SSRC change kept cached units")
	}
	c.observe(pion.MimeTypeH265, gopTestFrame(6, pion.MimeTypeH265, true, &sequence, 11), true, false, now)
	c.invalidate()
	if units, _, _ := c.size(); units != 0 {
		t.Fatal("explicit invalidation kept cached units")
	}

	// Age: a long screen-share GOP is released, not served.
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, true, &sequence, 12), true, false, now)
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, false, &sequence, 13), false, false, now.Add(gopCacheMaxAge+time.Millisecond))
	if units, _, _ := c.size(); units != 0 {
		t.Fatal("stale GOP retained")
	}

	// Bytes and packets stay bounded however long the GOP runs.
	c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, true, &sequence, 14), true, false, now)
	for frame := uint32(15); frame < 4000; frame++ {
		c.observe(pion.MimeTypeH264, gopTestFrame(5, pion.MimeTypeH264, false, &sequence, frame), false, false, now)
		_, packets, bytes := c.size()
		if packets > gopCacheMaxPackets || bytes > gopCacheMaxBytes {
			t.Fatalf("cache exceeded bounds: %d packets, %d bytes", packets, bytes)
		}
	}
}

// chanVideoTrack is a publisher track fed by the test.
type chanVideoTrack struct {
	ch    chan *rtp.Packet
	ssrc  uint32
	codec pion.RTPCodecCapability
}

func (c *chanVideoTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	packet, ok := <-c.ch
	if !ok {
		return nil, nil, io.EOF
	}
	return packet, nil, nil
}
func (c *chanVideoTrack) RID() string     { return "" }
func (c *chanVideoTrack) SSRC() pion.SSRC { return pion.SSRC(c.ssrc) }
func (c *chanVideoTrack) ID() string      { return "cam-track" }
func (c *chanVideoTrack) Codec() pion.RTPCodecParameters {
	return pion.RTPCodecParameters{RTPCodecCapability: c.codec, PayloadType: 102}
}

type gopFixture struct {
	*videoLoopbackFixture
	track     *chanVideoTrack
	stop      chan struct{}
	done      sync.WaitGroup
	keyframes atomic.Int64
	answered  atomic.Int64 // upstream PLIs answered with a keyframe
	mime      string
}

type gopArrival struct {
	packet *rtp.Packet
	at     time.Time
}

// startGOPFixture runs an H.264 publisher at 60 fps with a keyframe at start
// and every keyEvery (0: only on upstream PLI) through the production
// ReadVideoLoop, and one real recipient that has not yet watched.
func startGOPFixture(t *testing.T, keyEvery time.Duration, mimes ...string) (*gopFixture, chan gopArrival) {
	t.Helper()
	mime := pion.MimeTypeH264
	if len(mimes) > 0 {
		mime = mimes[0]
	}
	codec := pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: chaosH264Fmtp}
	if mime == pion.MimeTypeH265 {
		codec = pion.RTPCodecCapability{MimeType: pion.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: chaosH265Fmtp}
	}
	f := &gopFixture{videoLoopbackFixture: newVideoLoopbackFixture(t, SlotCam), stop: make(chan struct{}), mime: mime,
		track: &chanVideoTrack{ch: make(chan *rtp.Packet, 1024), ssrc: 4242, codec: codec}}
	if _, err := f.router.PublishVideoWithFormats("pub", SlotCam, 0, true, "", []string{"h265", "h264", "vp8"}, chaosHEVCLevel); err != nil {
		t.Fatal(err)
	}
	arrivals := make(chan gopArrival, 8192)
	f.receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		if track.Kind() != pion.RTPCodecTypeVideo {
			return
		}
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if len(packet.Payload) == 0 {
				continue
			}
			select {
			case arrivals <- gopArrival{packet, time.Now()}:
			default:
			}
		}
	})
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); f.router.ReadVideoLoop("pub", SlotCam, f.track) }()
	f.done.Add(1)
	go func() {
		defer f.done.Done()
		tick := time.NewTicker(time.Second / 60)
		defer tick.Stop()
		var sequence uint16 = 100
		lastKey := time.Now()
		plis := 0
		for frame := uint32(1); ; frame++ {
			select {
			case <-f.stop:
				return
			case <-tick.C:
			}
			upstream, _ := f.upstream.counts()
			key := frame == 1 || (keyEvery > 0 && time.Since(lastKey) >= keyEvery) || upstream > plis
			if upstream > plis {
				f.answered.Add(int64(upstream - plis))
				plis = upstream
			}
			if key {
				lastKey = time.Now()
				f.keyframes.Add(1)
			}
			for _, packet := range gopTestFrame(f.track.ssrc, f.mime, key, &sequence, frame) {
				select {
				case f.track.ch <- packet:
				case <-f.stop:
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		f.done.Wait()
		close(f.track.ch)
		<-readerDone
	})
	return f, arrivals
}

// watch starts the recipient's watch, negotiates its H.264 output and waits
// for the transport.
func (f *gopFixture) watch(t *testing.T) {
	t.Helper()
	generation := f.router.VideoPublications("viewer")[0].Generation
	if _, err := f.router.WatchVideoWithFormats("viewer", "pub", SlotCam, generation, 1, f.router.VideoWatchSession("viewer"), true, []string{"h265", "h264", "vp8"}, chaosHEVCLevel); err != nil {
		t.Fatal(err)
	}
	f.connect(t, SlotCam)
	if mime := f.output.track.Codec().MimeType; mime != f.mime {
		t.Fatalf("recipient output is %s", mime)
	}
}

// receive collects arrivals for d and checks that the first packet starts a
// keyframe and sequence numbers never go backwards or skip.
func receiveDecodable(t *testing.T, arrivals chan gopArrival, d time.Duration, mimes ...string) []gopArrival {
	t.Helper()
	var got []gopArrival
	deadline := time.After(d)
	for {
		select {
		case a := <-arrivals:
			got = append(got, a)
		case <-deadline:
			if len(got) == 0 {
				t.Fatal("recipient received no video")
			}
			mime := pion.MimeTypeH264
			if len(mimes) > 0 {
				mime = mimes[0]
			}
			if !chaosKeyStart(mime, got[0].packet.Payload) {
				t.Fatalf("first packet is not a keyframe start: % x", got[0].packet.Payload[:4])
			}
			for i := 1; i < len(got); i++ {
				if delta := rtpSequenceDelta(got[i].packet.SequenceNumber, got[i-1].packet.SequenceNumber); delta != 1 {
					t.Fatalf("sequence discontinuity %d -> %d at %d", got[i-1].packet.SequenceNumber, got[i].packet.SequenceNumber, i)
				}
			}
			return got
		}
	}
}

// A recipient joining 300 ms after a keyframe starts from the GOP cache: the
// publisher sees no keyframe request, and the first frame arrives at once.
func TestGOPCacheServesJoinWithoutUpstreamPLI(t *testing.T) {
	for _, mime := range []string{pion.MimeTypeH264, pion.MimeTypeH265} {
		t.Run(mime, func(t *testing.T) { testGOPCacheServesJoin(t, mime) })
	}
}

func testGOPCacheServesJoin(t *testing.T, mime string) {
	verifyNoLeaks(t)
	f, arrivals := startGOPFixture(t, 0, mime)
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	f.watch(t)
	got := receiveDecodable(t, arrivals, time.Second, mime)
	if wait := got[0].at.Sub(start); wait > 800*time.Millisecond {
		t.Fatalf("first keyframe after %v", wait)
	}
	if plis, _ := f.upstream.counts(); plis != 0 || f.keyframes.Load() != 1 {
		t.Fatalf("join forced %d upstream keyframe requests (%d keyframes)", plis, f.keyframes.Load())
	}
}

// With only a stale keyframe (older than the age cap) the join asks the
// publisher exactly once, and the answering keyframe starts the recipient.
func TestGOPCacheStaleKeyframeRequestsOnce(t *testing.T) {
	verifyNoLeaks(t)
	f, arrivals := startGOPFixture(t, 0)
	time.Sleep(gopCacheMaxAge + 300*time.Millisecond)
	f.watch(t)
	receiveDecodable(t, arrivals, 1500*time.Millisecond)
	if plis, _ := f.upstream.counts(); plis != 1 {
		t.Fatalf("stale cache join sent %d upstream keyframe requests, want 1", plis)
	}
}

// One recipient's PLI replays the cached GOP to it alone, continuing its
// sequence numbers, without an upstream request.
func TestGOPCacheServesViewerPLI(t *testing.T) {
	verifyNoLeaks(t)
	f, arrivals := startGOPFixture(t, 1500*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	f.watch(t)
	got := receiveDecodable(t, arrivals, 500*time.Millisecond)
	keyframes := f.keyframes.Load()
	last := got[len(got)-1].packet.SequenceNumber
	if err := f.receiver.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: f.output.egress.outputSSRC.Load()}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	replayed := false
	for !replayed {
		select {
		case a := <-arrivals:
			if delta := rtpSequenceDelta(a.packet.SequenceNumber, last); delta != 1 {
				t.Fatalf("refill broke sequence continuity: %d -> %d", last, a.packet.SequenceNumber)
			}
			last = a.packet.SequenceNumber
			replayed = chaosKeyStart(pion.MimeTypeH264, a.packet.Payload)
		case <-deadline:
			t.Fatal("viewer PLI was not answered from the GOP cache")
		}
	}
	if plis, _ := f.upstream.counts(); plis != 0 || f.keyframes.Load() > keyframes+1 {
		t.Fatalf("viewer PLI reached the publisher: %d requests", plis)
	}
}

// The cached burst must drain through the recipient's pacer within
// gopCacheMaxDrain; otherwise live packets queued behind it would expire.
// Such a recipient is not started from the cache and its continuity is kept.
func TestGOPInjectRespectsPacerDrain(t *testing.T) {
	var sequence uint16 = 10
	units := []gopUnit{}
	for frame := uint32(1); frame <= 120; frame++ {
		packets := gopTestFrame(7, pion.MimeTypeH264, frame == 1, &sequence, frame)
		// Accounted as 2 s of 60 fps at 15 Mbit/s: 31.25 kB per frame.
		units = append(units, gopUnit{packets: packets, bytes: 31_250, keyframe: frame == 1})
	}
	total := 0
	for _, unit := range units {
		total += unit.bytes
	}
	pacer := &mediaPacer{bitrate: 2_000_000} // 375 kB/s paced: ~10 s to drain
	output := &pubSlot{egress: &mediaEgressStream{pacer: pacer}}
	output.videoContinuity.seen, output.videoContinuity.sequence = true, 500
	before := output.videoContinuity
	if drain := pacer.videoDrain(total); drain <= gopCacheMaxDrain {
		t.Fatalf("fixture drains in %v", drain)
	}
	if started, advanced := NewRouter(nil).injectGOP(output, mediaTicket{}, pion.MimeTypeH264, units, time.Now()); started || advanced {
		t.Fatal("cache burst injected beyond the pacer's drain bound")
	}
	if output.videoContinuity != before || output.gopRefilledAt.Load() != 0 {
		t.Fatal("rejected injection changed the recipient's continuity")
	}
	pacer.bitrate = 50_000_000 // 9.4 MB/s paced
	if drain := pacer.videoDrain(total); drain > gopCacheMaxDrain {
		t.Fatalf("fast pacer drains in %v", drain)
	}
	if _, ok := (&mediaEgressStream{}).videoDrain(1); ok {
		t.Fatal("unbound output reported a pacer")
	}
}
