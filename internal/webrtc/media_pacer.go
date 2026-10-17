package webrtc

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"

	"noxa/internal/mediacc/gcc"
)

// A repaired upstream gap can release the whole reorder window at once.
// Keep it bounded while the existing congestion-paced worker drains it.
const mediaPacerPackets = videoReorderPackets
const mediaPacerBytes = videoReorderBytes
const mediaPacerLifetime = videoReorderWait + 500*time.Millisecond

// mediaPacerSustainedBurst is how far above the estimate the 1.5x pacing
// factor may run before output is held to the estimate itself, while the
// source persistently outruns the estimate.
const mediaPacerSustainedBurst = 40 * time.Millisecond

// Source overload is judged on the median of short input bins, so a one-off
// release (a repaired reorder gap, a keyframe) can still drain at 1.5x while
// a source that is steadily faster than this receiver's estimate cannot.
const mediaInputBin = 100 * time.Millisecond
const mediaInputBins = 10
const mediaOverloadRatio = 1.2
const mediaAudioPackets = 256
const mediaAudioBytes = 128 * 1024
const mediaAudioLifetime = 100 * time.Millisecond
const mediaRTXSequenceStates = 4096

var errMediaRTXSequenceLimit = errors.New("media pacer RTX sequence state limit reached")

// Retained for the peer connection lifetime, including unbind/rebind of an
// SSRC. Restarting its sequence under the same SRTP key could reuse an index.
// Only the final pacing worker advances next; binding only shares the pointer.
type mediaRTXSequence struct{ next uint16 }

// The pacer may spend up to this multiple of the estimate to absorb bursts.
const mediaPacingGain = 1.5

// Enough initial headroom for a Full HD stream while receiver feedback starts.
// This is not a floor: GCC may immediately reduce it on a constrained path.
const mediaInitialBitrate = 6_000_000

type pacedStream struct {
	writer      interceptor.RTPWriter
	active      bool // protected by mediaPacer.mu
	bindingID   string
	twccID      uint8
	audio       bool
	rtxSSRC     uint32
	rtxSequence *mediaRTXSequence
}
type pacedPacket struct {
	header      rtp.Header
	payload     []byte
	attrs       interceptor.Attributes
	stream      *pacedStream
	expires     time.Time
	size        int
	repair      mediaRepairKey
	kind        mediaPacketKind
	rtxSequence *mediaRTXSequence
}

// mediaPacer always queues, even with no backlog: StaticRTP holds its track
// lock during Write, so entering the authority there would invert teardown.
// The worker owns final writes. Close cancels queued work and joins it.
type mediaPacer struct {
	mu                                sync.Mutex
	streams                           map[uint32]*pacedStream
	queue                             []pacedPacket // allocated only after video admission
	head, count, bytes                int
	bitrate                           int
	closed                            bool
	stop, done                        chan struct{}
	egress                            *mediaEgressRegistry
	transportSequence                 uint16 // owned by the pacing worker
	historyMu                         sync.Mutex
	sent                              uint64 // historyMu covers insertion and feedback delivery
	audioQueue                        [mediaAudioPackets]pacedPacket
	audioHead, audioCount, audioBytes int
	// Voice has its own worker: it is woken per packet, never waits for the
	// video pacing tick, and never queues behind a video socket write.
	audioWake chan struct{} // protected by mu; created by run
	audioBusy bool          // protected by mu; voice worker is draining
	// Voice bytes written since the video worker last refilled its budget.
	// Video is paced with the estimate minus voice, never the reverse.
	audioSent       atomic.Int64
	diagnostics     mediaPacerCounters           // protected by mu; video only
	queuedRepairs   map[mediaRepairKey]struct{}  // protected by mu; bounded by video queue
	rtxSequences    map[uint32]*mediaRTXSequence // protected by mu; bounded, never evicted
	inputBins       [mediaInputBins]int          // protected by mu; primary video bytes per bin
	inputBin        int
	inputBinStart   time.Time
	videoStarted    atomic.Bool
	startVideoProbe func(int) bool // installed before binding a stream
}

func newMediaPacer() *mediaPacer {
	p := &mediaPacer{streams: make(map[uint32]*pacedStream), bitrate: mediaInitialBitrate, stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *mediaPacer) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if old := p.streams[ssrc]; old != nil {
		old.active = false
	}
	p.streams[ssrc] = &pacedStream{writer: writer, active: true}
}

func (p *mediaPacer) addRTX(ssrc, rtx uint32) {
	if rtx == 0 || rtx == ssrc {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if stream := p.streams[ssrc]; stream != nil {
		state := p.rtxSequences[rtx]
		if state == nil && len(p.rtxSequences) < mediaRTXSequenceStates {
			if p.rtxSequences == nil {
				p.rtxSequences = make(map[uint32]*mediaRTXSequence)
			}
			state = &mediaRTXSequence{next: rtp.NewRandomSequencer().NextSequenceNumber()}
			p.rtxSequences[rtx] = state
		}
		stream.rtxSSRC = rtx
		stream.rtxSequence = state
		p.streams[rtx] = stream
	}
}

func (p *mediaPacer) removeStream(ssrc, rtx uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stream := p.streams[ssrc]
	if stream == nil {
		return
	}
	stream.active = false
	delete(p.streams, ssrc)
	if p.streams[rtx] == stream {
		delete(p.streams, rtx)
	}
	count := p.count
	for range count {
		packet := p.popLocked()
		if packet.stream != stream {
			p.pushLocked(packet)
		}
	}
	count = p.audioCount
	for range count {
		packet := p.popAudioLocked()
		if packet.stream != stream {
			p.pushAudioLocked(packet)
		}
	}
}

func (p *mediaPacer) SetTargetBitrate(rate int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bitrate = min(max(rate, 0), 1_000_000_000)
}

// videoBacklog estimates how long queued video waits before the pacer can
// send it at its current pacing rate.
func (p *mediaPacer) videoBacklog() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	rate := float64(p.bitrate) * mediaPacingGain / 8
	if p.closed || p.count == 0 || rate <= 0 {
		return 0
	}
	return time.Duration(float64(p.bytes) / rate * float64(time.Second))
}

func (p *mediaPacer) Write(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	size := header.MarshalSize() + len(payload) + int(header.PaddingSize)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	stream := p.streams[header.SSRC]
	if stream == nil || !stream.active || size > 65535 {
		if stream == nil || !stream.audio {
			p.diagnostics.droppedRetired++
		}
		return size, nil
	}
	var rtxSequence *mediaRTXSequence
	if stream.rtxSSRC != 0 && header.SSRC == stream.rtxSSRC {
		rtxSequence = stream.rtxSequence
		if rtxSequence == nil {
			if !stream.audio {
				p.diagnostics.droppedRetired++
			}
			return 0, errMediaRTXSequenceLimit
		}
	}
	repair := queuedRepairKey(stream, header, payload)
	if repair.stream != nil {
		if _, queued := p.queuedRepairs[repair]; queued {
			p.diagnostics.coalescedRepairs++
			return size, nil
		}
	}
	if stream.audio {
		if p.audioCount == mediaAudioPackets || p.audioBytes+size > mediaAudioBytes {
			return size, nil
		}
	} else if p.count == mediaPacerPackets || p.bytes+size > mediaPacerBytes {
		p.diagnostics.droppedQueueFull++
		return size, nil
	}
	attributes := make(interceptor.Attributes, len(attrs))
	for key, value := range attrs {
		attributes[key] = value
	}
	packet := pacedPacket{header: header.Clone(), payload: append([]byte(nil), payload...), attrs: attributes, stream: stream, expires: time.Now().Add(mediaPacerLifetime), size: size, repair: repair}
	packet.rtxSequence = rtxSequence
	// Classify while stream metadata is protected; no packet content is retained
	// by diagnostics. Same-SSRC retransmissions remain primary, not proven RTX.
	if header.Padding && len(payload) == 0 {
		packet.kind = mediaPacketPadding
	} else if stream.rtxSSRC != 0 && header.SSRC == stream.rtxSSRC {
		packet.kind = mediaPacketRTX
	}
	if stream.audio {
		packet.expires = time.Now().Add(mediaAudioLifetime)
		p.pushAudioLocked(packet)
		select {
		case p.audioWake <- struct{}{}:
		default: // A wake-up is already pending; the worker drains everything.
		}
	} else {
		if packet.kind == mediaPacketPrimary {
			p.advanceInputLocked(time.Now())
			p.inputBins[p.inputBin] += size
		}
		p.pushLocked(packet)
	}
	return size, nil
}

func (p *mediaPacer) advanceInputLocked(now time.Time) {
	if p.inputBinStart.IsZero() || now.Sub(p.inputBinStart) >= mediaInputBin*mediaInputBins {
		clear(p.inputBins[:])
		p.inputBin, p.inputBinStart = 0, now
		return
	}
	for now.Sub(p.inputBinStart) >= mediaInputBin {
		p.inputBin = (p.inputBin + 1) % mediaInputBins
		p.inputBins[p.inputBin] = 0
		p.inputBinStart = p.inputBinStart.Add(mediaInputBin)
	}
}

// sourceOverloadedLocked reports whether the median video input rate over the
// last second exceeds the estimate by mediaOverloadRatio.
func (p *mediaPacer) sourceOverloadedLocked(now time.Time, estimate float64) bool {
	p.advanceInputLocked(now)
	bins := p.inputBins
	slices.Sort(bins[:])
	return float64(bins[mediaInputBins/2])/mediaInputBin.Seconds() > mediaOverloadRatio*estimate
}

func (p *mediaPacer) pushAudioLocked(packet pacedPacket) {
	p.audioQueue[(p.audioHead+p.audioCount)%mediaAudioPackets] = packet
	p.audioCount++
	p.audioBytes += packet.size
}

func (p *mediaPacer) popAudioLocked() pacedPacket {
	packet := p.audioQueue[p.audioHead]
	p.audioQueue[p.audioHead] = pacedPacket{}
	p.audioHead = (p.audioHead + 1) % mediaAudioPackets
	p.audioCount--
	p.audioBytes -= packet.size
	return packet
}

func (p *mediaPacer) pushLocked(packet pacedPacket) {
	if p.queue == nil {
		p.queue = make([]pacedPacket, mediaPacerPackets)
	}
	p.queue[(p.head+p.count)%mediaPacerPackets] = packet
	p.count++
	p.bytes += packet.size
	if packet.repair.stream != nil {
		if p.queuedRepairs == nil {
			p.queuedRepairs = make(map[mediaRepairKey]struct{})
		}
		p.queuedRepairs[packet.repair] = struct{}{}
	}
}
func (p *mediaPacer) popLocked() pacedPacket {
	packet := p.queue[p.head]
	p.queue[p.head] = pacedPacket{}
	p.head = (p.head + 1) % mediaPacerPackets
	p.count--
	p.bytes -= packet.size
	delete(p.queuedRepairs, packet.repair)
	return packet
}

func (p *mediaPacer) run() {
	defer close(p.done)
	p.mu.Lock()
	if p.audioWake == nil {
		p.audioWake = make(chan struct{}, 1)
	}
	wake := p.audioWake
	p.mu.Unlock()
	audioDone := make(chan struct{})
	go p.runAudio(wake, audioDone)
	defer func() { <-audioDone }() // Close joins both lanes through done.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	last := time.Now()
	budget := float64(0)    // burst credit at the 1.5x pacing factor
	sustained := float64(0) // average credit at 1.0x the estimate
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			p.mu.Lock()
			// Voice is written by runAudio and never waits for this worker. Its
			// bytes share the estimated path, so they are reserved out of the
			// video budget: video receives the estimate minus voice.
			// Bound catch-up after a stalled socket; never release a huge burst.
			elapsed := min(now.Sub(last), 50*time.Millisecond).Seconds()
			estimate := float64(p.bitrate) / 8
			bytesPerSecond := estimate * mediaPacingGain
			budget = min(budget+elapsed*bytesPerSecond, 0.05*bytesPerSecond)
			// The 1.5x factor drains frame bursts quickly, but a source that
			// outruns the estimate (one slow viewer of a 1080p60 share) must
			// not be forwarded at 1.5x indefinitely: that keeps a standing
			// queue and loss at the viewer's bottleneck, delaying and dropping
			// its voice. Sustained output is held to the estimate.
			sustained = min(sustained+elapsed*estimate, mediaPacerSustainedBurst.Seconds()*estimate)
			overloaded := p.sourceOverloadedLocked(now, estimate)
			if !overloaded {
				sustained = max(sustained, 0) // Catch-up debt only counts under overload.
			}
			// The voice debit is bounded so a long silence-free talk spurt on a
			// tiny estimate cannot build unbounded video debt; video's own packet
			// debt is never forgiven.
			voice := float64(p.audioSent.Swap(0))
			budget = max(budget-voice, min(budget, -0.05*bytesPerSecond))
			sustained = max(sustained-voice, min(sustained, -mediaPacerSustainedBurst.Seconds()*estimate))
			last = now
			for work := p.count; work > 0 && p.count > 0 && !p.closed; work-- {
				front := &p.queue[p.head]
				if !front.stream.active {
					p.diagnostics.droppedRetired++
					p.popLocked()
					continue
				}
				if !time.Now().Before(front.expires) {
					p.diagnostics.droppedExpired++
					p.popLocked()
					continue
				}
				if budget <= 0 || (overloaded && sustained <= 0) {
					break
				}
				// Yield the rest of this burst to pending voice: voice then
				// enters a congested bottleneck queue ahead of the burst,
				// right after it drained for a tick, instead of behind it.
				if p.audioCount > 0 || p.audioBusy {
					break
				}
				packet := p.popLocked()
				p.mu.Unlock()
				if p.writePacket(packet) {
					// Only an authorized write attempt spends network pacing credit.
					// Final permission/ticket rejection never reaches the writer.
					budget -= float64(packet.size)
					sustained -= float64(packet.size)
				}
				p.mu.Lock()
			}
			p.mu.Unlock()
		}
	}
}

// runAudio is the strict-priority voice lane. Voice bypasses pacing delay:
// each packet is written as soon as it is queued, concurrently with (never
// behind) video writes, and still crosses the same final authorization
// boundary in writePacket. The queue only absorbs a briefly stalled socket.
func (p *mediaPacer) runAudio(wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		p.mu.Lock()
		p.audioBusy = true
		for p.audioCount > 0 && !p.closed {
			packet := p.popAudioLocked()
			if !packet.stream.active || !time.Now().Before(packet.expires) {
				continue
			}
			p.mu.Unlock()
			if p.writePacket(packet) {
				p.audioSent.Add(int64(packet.size))
			}
			p.mu.Lock()
		}
		p.audioBusy = false
		p.mu.Unlock()
		select {
		case <-p.stop:
			return
		case <-wake:
		}
	}
}

func (p *mediaPacer) writePacket(packet pacedPacket) bool {
	attempted := false
	write := interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		attempted = true
		// Pion caches an RTX sequence with each original. RFC 4588 requires a
		// fresh sequence for every transmission, including repeated NACKs and
		// padding on that SSRC. Do this only after final authorization, on our
		// owned header, before GCC and SRTP. Consume even if the write fails.
		if state := packet.rtxSequence; state != nil {
			header.SequenceNumber = state.next
			state.next++
		}
		if packet.stream.twccID != 0 {
			p.historyMu.Lock()
			defer p.historyMu.Unlock()
			var extension [2]byte
			binary.BigEndian.PutUint16(extension[:], p.transportSequence)
			if err := header.SetExtension(packet.stream.twccID, extension[:]); err != nil {
				return 0, err
			}
			p.transportSequence++
		}
		// GCC accounts the send here, after queue expiry and final permission
		// checks. Locally discarded packets cannot manufacture network loss.
		n, err := packet.stream.writer.Write(header, payload, attrs)
		if packet.stream.twccID != 0 {
			p.sent++
		}
		return n, err
	})
	if p.egress == nil { // Standalone pacing tests have no router bindings.
		n, err := write.Write(&packet.header, packet.payload, packet.attrs)
		p.recordVideoWrite(packet, attempted, n, err, mediaDropUnclassified)
		return attempted
	}
	p.egress.mu.Lock()
	stream := p.egress.streams[packet.stream.bindingID]
	p.egress.mu.Unlock()
	if stream != nil {
		n, reason, err := stream.writeResultKind(&packet.header, packet.payload, packet.attrs, write, packet.kind)
		p.recordVideoWrite(packet, attempted, n, err, reason)
	} else {
		p.recordVideoWrite(packet, false, 0, nil, mediaDropMissingBinding)
	}
	return attempted
}

func (p *mediaPacer) Close() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		for _, stream := range p.streams {
			stream.active = false
		}
		clear(p.streams)
		clear(p.queue)
		p.queue = nil
		clear(p.queuedRepairs)
		p.count, p.bytes = 0, 0
		clear(p.audioQueue[:])
		p.audioCount, p.audioBytes = 0, 0
		close(p.stop)
	}
	p.mu.Unlock()
	<-p.done
	p.mu.Lock()
	clear(p.rtxSequences)
	p.mu.Unlock()
	return nil
}

// Each peer owns a paced GCC estimator; unbind removes both primary and RTX.
type mediaCCFactory struct{ registry *mediaEgressRegistry }

func (f mediaCCFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	pacer := newMediaPacer()
	pacer.egress = f.registry
	factory, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		estimator, err := gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(mediaInitialBitrate), gcc.SendSideBWEPacer(pacer))
		if err != nil {
			return nil, err
		}
		pacer.startVideoProbe = estimator.StartVideoProbe
		return &mediaBandwidthEstimator{BandwidthEstimator: estimator, pacer: pacer}, nil
	})
	if err != nil {
		_ = pacer.Close()
		return nil, err
	}
	inner, err := factory.NewInterceptor(id)
	if err != nil {
		_ = pacer.Close()
		return nil, err
	}
	return &mediaCCInterceptor{Interceptor: inner, pacer: pacer}, nil
}

type mediaCCInterceptor struct {
	interceptor.Interceptor
	pacer *mediaPacer
}

func (i *mediaCCInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	// GCC's AddStream registers the primary SSRC with the injected pacer.
	out := i.Interceptor.BindLocalStream(info, writer)
	i.pacer.mu.Lock()
	if stream := i.pacer.streams[info.SSRC]; stream != nil {
		stream.bindingID = info.ID
		stream.audio = strings.EqualFold(info.MimeType, "audio/opus")
		for _, extension := range info.RTPHeaderExtensions {
			if !stream.audio && extension.URI == sdp.TransportCCURI && extension.ID > 0 && extension.ID < 256 {
				stream.twccID = uint8(extension.ID)
			}
		}
	}
	i.pacer.mu.Unlock()
	i.pacer.addRTX(info.SSRC, info.SSRCRetransmission)
	if registry := i.pacer.egress; registry != nil {
		registry.mu.Lock()
		output := registry.streams[info.ID]
		registry.mu.Unlock()
		if output != nil {
			output.mu.Lock()
			output.pacer = i.pacer
			notify := output.onVideoReady
			output.mu.Unlock()
			if notify != nil && !strings.EqualFold(info.MimeType, "audio/opus") {
				notify()
			}
		}
	}
	return out
}
func (i *mediaCCInterceptor) UnbindLocalStream(info *interceptor.StreamInfo) {
	i.pacer.removeStream(info.SSRC, info.SSRCRetransmission)
	i.Interceptor.UnbindLocalStream(info)
}
