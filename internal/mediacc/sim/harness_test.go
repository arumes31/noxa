package sim

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"noxa/internal/mediacc/gcc"
	"noxa/internal/mediacc/keyframe"
)

// Model constants mirror the relay (internal/webrtc) where it matters:
//   - mediaInitialBitrate = 6 Mbit/s for every new viewer peer;
//   - mediaPacer: 5 ms ticks, 1.5x pacing gain, 50 ms burst ceiling, queue
//     lifetime videoReorderWait (2.5 s) + 500 ms;
//   - keyframeRequestWindow: one upstream PLI per source per second.
const (
	simInitialBitrate  = 6_000_000
	simPacerTick       = 5 * time.Millisecond
	simPacingGain      = 1.5
	simPacerLifetime   = 3 * time.Second
	simKeyframeWindow  = time.Second
	simTWCCInterval    = 50 * time.Millisecond
	simPacketPayload   = 1200
	simPacketOverhead  = 40
	simGiveUp          = time.Second
	simPLIRepeat       = 200 * time.Millisecond
	simNackRetries     = 10
	simRTXHistory      = 1500 * time.Millisecond
	simWindow          = 500 * time.Millisecond
	simGoodLatency     = 400 * time.Millisecond
	simTWCCExtensionID = 5
	simSSRC            = 0x1234
	simTransportCCURI  = "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"
)

var simEpoch = time.Unix(1_000_000, 0)

// ---------------------------------------------------------------------------
// Discrete-event core with a virtual clock.
// ---------------------------------------------------------------------------

type simEvent struct {
	at  time.Time
	seq uint64
	fn  func()
}

type eventHeap []*simEvent

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if !h[i].at.Equal(h[j].at) {
		return h[i].at.Before(h[j].at)
	}
	return h[i].seq < h[j].seq
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(*simEvent)) }
func (h *eventHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

type simTimer struct{ stopped bool }

func (t *simTimer) Stop() bool {
	was := !t.stopped
	t.stopped = true
	return was
}

type world struct {
	// gateKeyframes enables the relay's receiver keyframe gate in the model.
	gateKeyframes bool

	now     time.Time
	events  eventHeap
	seq     uint64
	rng     *rand.Rand
	source  *source
	viewers []*viewer
}

func newWorld(seed int64) *world {
	return &world{now: simEpoch, rng: rand.New(rand.NewSource(seed)), gateKeyframes: true}
}

func (w *world) elapsed() time.Duration { return w.now.Sub(simEpoch) }

func (w *world) at(t time.Time, fn func()) {
	w.seq++
	heap.Push(&w.events, &simEvent{at: t, seq: w.seq, fn: fn})
}

func (w *world) after(d time.Duration, fn func()) { w.at(w.now.Add(d), fn) }

func (w *world) every(period time.Duration, fn func() bool) {
	var tick func()
	tick = func() {
		if fn() {
			w.after(period, tick)
		}
	}
	w.after(period, tick)
}

func (w *world) afterFunc(d time.Duration, f func()) gcc.Timer {
	timer := &simTimer{}
	w.after(d, func() {
		if !timer.stopped {
			timer.stopped = true
			f()
		}
	})
	return timer
}

func (w *world) run(until time.Duration) {
	end := simEpoch.Add(until)
	for w.events.Len() > 0 {
		next := w.events[0]
		if next.at.After(end) {
			break
		}
		heap.Pop(&w.events)
		w.now = next.at
		next.fn()
	}
	w.now = end
}

// ---------------------------------------------------------------------------
// Publisher: one encoded upload (single codec, single layer) at a demand rate.
// Keyframes are produced on (coalesced) relay requests. The publisher's own
// upload is assumed clean; the relay is the system under test.
// ---------------------------------------------------------------------------

type mediaPacket struct {
	seq     int
	frame   int
	key     bool
	count   int
	size    int
	capture time.Time
	sent    time.Time // arrival at the relay
}

type source struct {
	w              *world
	fps            int
	demand         func(time.Duration) int
	keyframeFactor float64
	ingress        time.Duration

	frame, seq      int
	keyframePending bool
	lastRequest     time.Time
	history         []*mediaPacket // relay-side RTX cache, bounded by age

	keyframeRequests int // requests that reached the relay (any viewer)
	keyframesForced  int // requests forwarded to the publisher (PLIs)
	keyframeByReason map[string]int
	bytes            []rateSample
}

type rateSample struct {
	at    time.Time
	bytes int
}

func newSource(w *world, fps int, demand func(time.Duration) int) *source {
	s := &source{w: w, fps: fps, demand: demand, keyframeFactor: 8, ingress: 20 * time.Millisecond,
		keyframePending: true, keyframeByReason: map[string]int{}}
	w.source = s
	interval := time.Second / time.Duration(fps)
	w.every(interval, func() bool { s.produce(); return true })
	return s
}

// requestKeyframe models Router.requestKeyframe/allowKeyframeRequest: one
// upstream PLI per source per keyframeRequestWindow, regardless of requester.
func (s *source) requestKeyframe(reason string) {
	s.keyframeRequests++
	if !s.lastRequest.IsZero() && s.w.now.Sub(s.lastRequest) < simKeyframeWindow {
		return
	}
	s.lastRequest = s.w.now
	s.keyframesForced++
	s.keyframeByReason[reason]++
	// Relay -> publisher, plus encode: the next frame after that is an IDR.
	s.w.after(s.ingress+5*time.Millisecond, func() { s.keyframePending = true })
}

// measuredRate is the relay's ingress demand sample (videoStartupDemand).
func (s *source) measuredRate() int {
	cutoff := s.w.now.Add(-time.Second)
	total := 0
	for _, sample := range s.bytes {
		if sample.at.After(cutoff) {
			total += sample.bytes
		}
	}
	return total * 8
}

func (s *source) produce() {
	now := s.w.now
	rate := s.demand(s.w.elapsed())
	if rate <= 0 {
		return
	}
	avg := float64(rate) / 8 / float64(s.fps)
	size := avg * (0.75 + 0.5*s.w.rng.Float64())
	key := s.keyframePending
	if key {
		size = avg * s.keyframeFactor
		s.keyframePending = false
	}
	count := max(1, int(math.Ceil(size/simPacketPayload)))
	capture := now.Add(-s.ingress)
	packets := make([]*mediaPacket, count)
	for i := range packets {
		packets[i] = &mediaPacket{seq: s.seq, frame: s.frame, key: key, count: count,
			size: simPacketPayload + simPacketOverhead, capture: capture, sent: now}
		s.seq++
	}
	s.frame++
	s.bytes = append(s.bytes, rateSample{at: now, bytes: count * (simPacketPayload + simPacketOverhead)})
	if len(s.bytes) > 4*s.fps {
		s.bytes = s.bytes[len(s.bytes)-2*s.fps:]
	}
	s.history = append(s.history, packets...)
	cut := 0
	for cut < len(s.history) && now.Sub(s.history[cut].sent) > simRTXHistory {
		cut++
	}
	s.history = s.history[cut:]
	for _, v := range s.w.viewers {
		if v.active {
			v.forward(packets)
		}
	}
}

func (s *source) lookup(seq int) *mediaPacket {
	i := sort.Search(len(s.history), func(i int) bool { return s.history[i].seq >= seq })
	if i < len(s.history) && s.history[i].seq == seq {
		return s.history[i]
	}
	return nil
}

// ---------------------------------------------------------------------------
// Access link: a FIFO bottleneck with drop-tail queue, propagation delay,
// in-order delivery jitter (aggregation/Wi-Fi style spikes) and Gilbert loss.
// ---------------------------------------------------------------------------

type linkConfig struct {
	capacity     func(time.Duration) int // bit/s
	oneWay       time.Duration
	jitter       time.Duration // uniform per packet extra delay
	spikeEvery   time.Duration // mean interval between delay spikes (0: none)
	spikeDelay   [2]time.Duration
	spikeLength  [2]time.Duration
	lossBurst    float64 // mean loss burst length in packets (Gilbert)
	lossRate     float64 // stationary loss probability
	maxQueue     time.Duration
	returnJitter time.Duration
}

type link struct {
	w        *world
	cfg      linkConfig
	freeAt   time.Time
	last     time.Time
	bad      bool
	spikeEnd time.Time
	spikeAdd time.Duration
	nextKick time.Time
	dropped  int
	lost     int
}

func newLink(w *world, cfg linkConfig) *link {
	if cfg.maxQueue == 0 {
		cfg.maxQueue = 250 * time.Millisecond
	}
	l := &link{w: w, cfg: cfg}
	l.scheduleSpike()
	return l
}

func (l *link) scheduleSpike() {
	if l.cfg.spikeEvery <= 0 {
		return
	}
	gap := time.Duration(l.w.rng.ExpFloat64() * float64(l.cfg.spikeEvery))
	l.nextKick = l.w.now.Add(gap)
}

func uniform(r *rand.Rand, lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(r.Int63n(int64(hi-lo)))
}

func (l *link) extraDelay(now time.Time) time.Duration {
	if l.cfg.spikeEvery > 0 && !now.Before(l.nextKick) {
		l.spikeAdd = uniform(l.w.rng, l.cfg.spikeDelay[0], l.cfg.spikeDelay[1])
		l.spikeEnd = now.Add(uniform(l.w.rng, l.cfg.spikeLength[0], l.cfg.spikeLength[1]))
		l.scheduleSpike()
	}
	d := time.Duration(0)
	if l.cfg.jitter > 0 {
		d = uniform(l.w.rng, 0, l.cfg.jitter)
	}
	if now.Before(l.spikeEnd) {
		d += l.spikeAdd
	}
	return d
}

func (l *link) lose() bool {
	if l.cfg.lossRate <= 0 {
		return false
	}
	burst := max(1, l.cfg.lossBurst)
	exit := 1 / burst
	enter := l.cfg.lossRate * exit / (1 - l.cfg.lossRate)
	if l.bad {
		if l.w.rng.Float64() < exit {
			l.bad = false
		}
	} else if l.w.rng.Float64() < enter {
		l.bad = true
	}
	return l.bad
}

// transmit returns false when the packet is dropped; deliver runs at arrival.
func (l *link) transmit(size int, deliver func(arrival time.Time)) {
	now := l.w.now
	start := now
	if l.freeAt.After(start) {
		start = l.freeAt
	}
	if start.Sub(now) > l.cfg.maxQueue {
		l.dropped++
		return
	}
	capacity := max(l.cfg.capacity(start.Sub(simEpoch)), 100_000)
	l.freeAt = start.Add(time.Duration(float64(size*8) / float64(capacity) * float64(time.Second)))
	if l.lose() {
		l.lost++
		return
	}
	arrival := l.freeAt.Add(l.cfg.oneWay + l.extraDelay(now))
	if arrival.Before(l.last) {
		arrival = l.last // in-order delivery: a delayed packet holds later ones
	}
	l.last = arrival
	l.w.at(arrival, func() { deliver(arrival) })
}

func (l *link) returnDelay() time.Duration {
	return l.cfg.oneWay + uniform(l.w.rng, 0, l.cfg.returnJitter)
}

// ---------------------------------------------------------------------------
// Viewer: production GCC estimator + relay pacer model + receiver model.
// ---------------------------------------------------------------------------

type queued struct {
	packet  *mediaPacket
	rtx     bool
	expires time.Time
}

type rxFrame struct {
	count, got int
	key        bool
	capture    time.Time
}

type nackState struct {
	next  time.Time
	tries int
}

type viewer struct {
	w      *world
	name   string
	link   *link
	bwe    *gcc.SendSideBWE
	active bool
	joined time.Time

	// pacer model (internal/webrtc.mediaPacer)
	writer      interceptor.RTPWriter
	target      int
	queue       []queued
	queuedRTX   map[int]bool
	budget      float64
	lastTick    time.Time
	twccSeq     uint16
	sending     *queued
	probeTried  bool
	payload     []byte
	recorder    *twcc.Recorder
	expired     int
	sentBytes   int
	mediaBytes  int
	rtxBytes    int
	firstPacket bool

	// receiver model
	got        map[int]bool
	highest    int
	missing    map[int]*nackState
	frames     map[int]*rxFrame
	nextFrame  int
	started    bool
	broken     bool
	stuckSince time.Time
	lastPLI    time.Time
	lastRender time.Time
	plis       int
	gate       keyframe.FeedbackGate
	gated      bool // model the relay's receiver keyframe gate (Router.relayKeyframeRequest)

	// metrics
	rendered       int
	freezes        int
	freezeTime     time.Duration
	windowFrames   int
	windowLatency  time.Duration
	windows        []window
	targets        []targetSample
	firstGoodAfter map[string]time.Duration
}

type window struct {
	end  time.Duration
	good bool
}

type targetSample struct {
	at     time.Duration
	target int
	demand int
}

func (w *world) addViewer(name string, cfg linkConfig) *viewer {
	v := &viewer{w: w, name: name, link: newLink(w, cfg), active: true, joined: w.now, gated: w.gateKeyframes,
		target: simInitialBitrate, queuedRTX: map[int]bool{}, payload: make([]byte, 1500),
		recorder: twcc.NewRecorder(0xbeef), got: map[int]bool{}, highest: -1,
		missing: map[int]*nackState{}, frames: map[int]*rxFrame{}, firstPacket: true,
		firstGoodAfter: map[string]time.Duration{}}
	bwe, err := gcc.NewSendSideBWE(
		gcc.SendSideBWEInitialBitrate(simInitialBitrate),
		gcc.SendSideBWEPacer(v),
		gcc.SendSideBWEClock(func() time.Time { return w.now }, w.afterFunc),
	)
	if err != nil {
		panic(err)
	}
	v.bwe = bwe
	bwe.AddStream(&interceptor.StreamInfo{SSRC: simSSRC, MimeType: "video/H264",
		RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{URI: simTransportCCURI, ID: simTWCCExtensionID}}}, interceptor.RTPWriterFunc(v.transmit))
	w.viewers = append(w.viewers, v)
	v.lastTick = w.now
	w.every(simPacerTick, func() bool { v.pace(); return v.active })
	w.every(simTWCCInterval, func() bool { v.sendFeedback(); return v.active })
	w.every(10*time.Millisecond, func() bool { v.receiverTick(); return v.active })
	w.every(100*time.Millisecond, func() bool { v.sample(); return v.active })
	w.every(simWindow, func() bool { v.closeWindow(); return v.active })
	if w.source != nil {
		// A watch start requests a keyframe from the publisher (keyframeWatchStart).
		w.source.requestKeyframe("watch-start")
	}
	return v
}

func (v *viewer) leave() {
	v.active = false
	_ = v.bwe.Close()
}

// gcc.Pacer implementation: the estimator registers its accounting writer and
// sets the target; media enters through forward().
func (v *viewer) AddStream(_ uint32, writer interceptor.RTPWriter) { v.writer = writer }
func (v *viewer) SetTargetBitrate(rate int)                        { v.target = rate }
func (v *viewer) Close() error                                     { return nil }
func (v *viewer) Write(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
	return 0, nil
}

func (v *viewer) forward(packets []*mediaPacket) {
	now := v.w.now
	for _, p := range packets {
		v.queue = append(v.queue, queued{packet: p, expires: now.Add(simPacerLifetime)})
		v.mediaBytes += p.size
	}
	// startVideoProbe: the first admitted media packet probes measured demand once.
	if v.firstPacket {
		v.firstPacket = false
		v.bwe.StartVideoProbe(v.w.source.measuredRate())
	}
}

// backlog mirrors mediaPacer.videoBacklog: queued bytes at the paced rate.
func (v *viewer) backlog() time.Duration {
	rate := float64(v.target) * simPacingGain / 8
	if len(v.queue) == 0 || rate <= 0 {
		return 0
	}
	bytes := 0
	for _, q := range v.queue {
		bytes += q.packet.size
	}
	return time.Duration(float64(bytes) / rate * float64(time.Second))
}

func (v *viewer) enqueueRTX(p *mediaPacket) {
	if v.queuedRTX[p.seq] {
		return // mediaPacer coalesces an already queued repair
	}
	v.queuedRTX[p.seq] = true
	v.queue = append(v.queue, queued{packet: p, rtx: true, expires: v.w.now.Add(simPacerLifetime)})
}

func (v *viewer) pace() {
	now := v.w.now
	bytesPerSecond := float64(v.target) * simPacingGain / 8
	elapsed := min(now.Sub(v.lastTick), 50*time.Millisecond)
	v.budget = min(v.budget+elapsed.Seconds()*bytesPerSecond, 0.05*bytesPerSecond)
	v.lastTick = now
	for len(v.queue) > 0 {
		front := v.queue[0]
		if !now.Before(front.expires) {
			v.queue = v.queue[1:]
			if front.rtx {
				delete(v.queuedRTX, front.packet.seq)
			}
			v.expired++
			continue
		}
		if v.budget <= 0 {
			break
		}
		v.queue = v.queue[1:]
		if front.rtx {
			delete(v.queuedRTX, front.packet.seq)
		}
		v.send(&front)
		v.budget -= float64(front.packet.size)
	}
}

func (v *viewer) send(q *queued) {
	header := &rtp.Header{Version: 2, PayloadType: 102, SSRC: simSSRC, SequenceNumber: uint16(q.packet.seq)}
	ext, _ := (&rtp.TransportCCExtension{TransportSequence: v.twccSeq}).Marshal()
	_ = header.SetExtension(simTWCCExtensionID, ext)
	v.twccSeq++
	v.sending = q
	payload := v.payload[:max(0, q.packet.size-header.MarshalSize())]
	if _, err := v.writer.Write(header, payload, interceptor.Attributes{}); err != nil {
		panic(err)
	}
	v.sending = nil
}

// transmit is the network writer behind the estimator's OnSent accounting.
func (v *viewer) transmit(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	q := *v.sending
	size := header.MarshalSize() + len(payload)
	v.sentBytes += size
	if q.rtx {
		v.rtxBytes += size
	}
	var ext rtp.TransportCCExtension
	_ = ext.Unmarshal(header.GetExtension(simTWCCExtensionID))
	seq := ext.TransportSequence
	v.link.transmit(size, func(arrival time.Time) { v.receive(q.packet, seq, arrival) })
	return size, nil
}

func (v *viewer) sendFeedback() {
	packets := v.recorder.BuildFeedbackPacket()
	if len(packets) == 0 {
		return
	}
	v.w.after(v.link.returnDelay(), func() {
		if v.active {
			_ = v.bwe.WriteRTCP(packets, nil)
		}
	})
}

func (v *viewer) receive(p *mediaPacket, twccSeq uint16, arrival time.Time) {
	if !v.active {
		return
	}
	v.recorder.Record(simSSRC, twccSeq, arrival.Sub(simEpoch).Microseconds()+1_000_000)
	if v.got[p.seq] {
		return
	}
	v.got[p.seq] = true
	delete(v.missing, p.seq)
	if v.highest >= 0 && p.seq > v.highest+1 {
		for s := v.highest + 1; s < p.seq; s++ {
			if !v.got[s] {
				v.missing[s] = &nackState{next: arrival}
			}
		}
	}
	v.highest = max(v.highest, p.seq)
	if !v.started {
		// Decoding starts at the first frame seen; it needs a keyframe.
		v.started, v.nextFrame, v.broken = true, p.frame, true
	}
	if p.frame < v.nextFrame {
		return
	}
	f := v.frames[p.frame]
	if f == nil {
		f = &rxFrame{count: p.count, key: p.key, capture: p.capture}
		v.frames[p.frame] = f
	}
	f.got++
	v.decode()
}

func (v *viewer) receiverTick() {
	now := v.w.now
	var nacks []int
	for seq, state := range v.missing {
		if state.tries >= simNackRetries {
			continue
		}
		if !now.Before(state.next) {
			state.tries++
			state.next = now.Add(60 * time.Millisecond)
			nacks = append(nacks, seq)
		}
	}
	if len(nacks) > 0 {
		sort.Ints(nacks)
		v.w.after(v.link.returnDelay(), func() {
			if !v.active {
				return
			}
			for _, seq := range nacks {
				if p := v.w.source.lookup(seq); p != nil {
					v.enqueueRTX(p)
				}
			}
		})
	}
	v.decode()
	if v.started && v.broken && now.Sub(v.lastPLI) >= simPLIRepeat {
		v.lastPLI = now
		v.plis++
		v.w.after(v.link.returnDelay(), func() {
			if !v.active {
				return
			}
			if v.gated && !v.gate.Allow(v.w.now, v.backlog()) {
				return
			}
			v.w.source.requestKeyframe("viewer-" + v.name)
		})
	}
}

func (v *viewer) decode() {
	now := v.w.now
	for v.started {
		f := v.frames[v.nextFrame]
		if f != nil && f.got >= f.count {
			delete(v.frames, v.nextFrame)
			v.nextFrame++
			v.stuckSince = time.Time{}
			if v.broken && !f.key {
				continue
			}
			v.broken = false
			v.render(f)
			continue
		}
		// A newer complete keyframe resets the dependency chain.
		newest := -1
		for id, candidate := range v.frames {
			if id > v.nextFrame && candidate.key && candidate.got >= candidate.count && id > newest {
				newest = id
			}
		}
		if newest >= 0 {
			v.skipTo(newest)
			continue
		}
		later := false
		for id := range v.frames {
			if id > v.nextFrame {
				later = true
				break
			}
		}
		if !later {
			return
		}
		if v.stuckSince.IsZero() {
			v.stuckSince = now
		}
		if now.Sub(v.stuckSince) < simGiveUp {
			return
		}
		// Unrecoverable: drop the frame and wait for a keyframe (PLI).
		v.skipTo(v.nextFrame + 1)
		v.broken = true
	}
}

func (v *viewer) skipTo(frame int) {
	for id := range v.frames {
		if id < frame {
			delete(v.frames, id)
		}
	}
	for seq := range v.missing {
		if seq < v.highest-4000 {
			delete(v.missing, seq)
		}
	}
	v.nextFrame = frame
	v.stuckSince = time.Time{}
}

func (v *viewer) render(f *rxFrame) {
	now := v.w.now
	frameInterval := time.Second / time.Duration(v.w.source.fps)
	if !v.lastRender.IsZero() {
		if gap := now.Sub(v.lastRender); gap > 200*time.Millisecond {
			v.freezes++
			v.freezeTime += gap - frameInterval
		}
	}
	v.lastRender = now
	v.rendered++
	v.windowFrames++
	v.windowLatency = max(v.windowLatency, now.Sub(f.capture))
	// prune receive bookkeeping
	if len(v.got) > 20000 {
		for seq := range v.got {
			if seq < v.highest-10000 {
				delete(v.got, seq)
			}
		}
	}
}

func (v *viewer) sample() {
	v.targets = append(v.targets, targetSample{at: v.w.elapsed(), target: v.bwe.GetTargetBitrate(),
		demand: v.w.source.demand(v.w.elapsed())})
}

func (v *viewer) closeWindow() {
	expected := float64(v.w.source.fps) * simWindow.Seconds()
	stalled := !v.lastRender.IsZero() && v.w.now.Sub(v.lastRender) > 200*time.Millisecond
	good := float64(v.windowFrames) >= 0.9*expected && v.windowLatency < simGoodLatency && !stalled
	v.windows = append(v.windows, window{end: v.w.elapsed(), good: good})
	v.windowFrames, v.windowLatency = 0, 0
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

type viewerReport struct {
	name          string
	fullQuality   float64 // fraction of 500 ms windows rendering >=90% fps under 400 ms latency
	freezes       int
	freezeTime    time.Duration
	starvedTime   time.Duration // time the pacer could not carry demand (1.5*target < demand)
	targetDrops   int           // >=10% downward estimate steps (100 ms samples)
	plis          int
	expired       int
	overhead      float64 // (sent - unique media) / unique media
	linkDrops     int
	minTarget     int
	meanTarget    int
	sentMbps      float64
	recoveryAfter map[string]time.Duration
}

func (v *viewer) report(from time.Duration) viewerReport {
	r := viewerReport{name: v.name, freezes: v.freezes, freezeTime: v.freezeTime, plis: v.plis,
		expired: v.expired, linkDrops: v.link.dropped + v.link.lost, minTarget: math.MaxInt,
		recoveryAfter: map[string]time.Duration{}}
	good, total := 0, 0
	for _, win := range v.windows {
		if win.end <= from {
			continue
		}
		total++
		if win.good {
			good++
		}
	}
	if total > 0 {
		r.fullQuality = float64(good) / float64(total)
	}
	var sum, count int
	for i, s := range v.targets {
		if s.at <= from {
			continue
		}
		if float64(s.target)*simPacingGain < float64(s.demand) {
			r.starvedTime += 100 * time.Millisecond
		}
		if i > 0 && float64(s.target) < 0.9*float64(v.targets[i-1].target) {
			r.targetDrops++
		}
		r.minTarget = min(r.minTarget, s.target)
		sum += s.target
		count++
	}
	if count > 0 {
		r.meanTarget = sum / count
	}
	if v.mediaBytes > 0 {
		r.overhead = float64(v.sentBytes-v.mediaBytes) / float64(v.mediaBytes)
	}
	if d := v.w.elapsed() - v.joined.Sub(simEpoch); d > 0 {
		r.sentMbps = float64(v.sentBytes*8) / d.Seconds() / 1e6
	}
	return r
}

// recovery returns how long after event the viewer first reached a run of
// `sustain` consecutive good windows (-1 if never).
func (v *viewer) recovery(event, sustain time.Duration) time.Duration {
	need := int(sustain / simWindow)
	run := 0
	for _, win := range v.windows {
		if win.end <= event {
			continue
		}
		if win.good {
			run++
			if run >= need {
				return win.end - time.Duration(need)*simWindow - event
			}
		} else {
			run = 0
		}
	}
	return -1
}

// downStepsInStableWindow counts estimate decreases; used to detect periodic
// oscillation on a link whose capacity comfortably exceeds demand.
func (v *viewer) estimateDecreases(from, to time.Duration) int {
	n := 0
	for i := 1; i < len(v.targets); i++ {
		s := v.targets[i]
		if s.at <= from || s.at > to {
			continue
		}
		if s.target < v.targets[i-1].target {
			n++
		}
	}
	return n
}

func (r viewerReport) String() string {
	return fmt.Sprintf("%-8s fullQ=%5.1f%% freezes=%3d (%6.2fs) starved=%6.1fs drops=%3d plis=%4d expired=%5d minT=%5.2fM meanT=%5.2fM sent=%5.2fM overhead=%4.1f%% linkLoss=%d",
		r.name, 100*r.fullQuality, r.freezes, r.freezeTime.Seconds(), r.starvedTime.Seconds(), r.targetDrops, r.plis,
		r.expired, float64(r.minTarget)/1e6, float64(r.meanTarget)/1e6, r.sentMbps, 100*r.overhead, r.linkDrops)
}

func (s *source) String() string {
	var reasons []string
	for reason, n := range s.keyframeByReason {
		reasons = append(reasons, fmt.Sprintf("%s=%d", reason, n))
	}
	sort.Strings(reasons)
	return fmt.Sprintf("publisher keyframes forced=%d (requests=%d) [%s]", s.keyframesForced, s.keyframeRequests, strings.Join(reasons, " "))
}

// timeline renders the estimate as a compact per-second trace for debugging.
func (v *viewer) timeline(from, to time.Duration) string {
	var b strings.Builder
	for _, s := range v.targets {
		if s.at <= from || s.at > to || s.at%time.Second != 0 {
			continue
		}
		fmt.Fprintf(&b, "%d:%.1f ", int(s.at.Seconds()), float64(s.target)/1e6)
	}
	return b.String()
}

var _ rtcp.Packet = (*rtcp.TransportLayerCC)(nil)
