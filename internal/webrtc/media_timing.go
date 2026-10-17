package webrtc

import (
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Sender Report timing.
//
// Receivers synchronize a publisher's audio and video (lip sync) by mapping
// each stream's RTP timestamps onto one NTP capture clock, using the RTCP
// Sender Reports (SR) they receive. Pion's default SR generator maps the
// timestamp of the most recently forwarded packet to the SERVER's wall clock
// at forwarding time. That mapping includes upstream network jitter and every
// server-side buffering delay (the video reorder window can hold frames for
// up to 2.5 s while audio is forwarded at once), so it skews A/V sync.
//
// Instead the server keeps the publisher's own mapping. It reads the SRs the
// publisher sends for each inbound SSRC (sourceTimingTable) and, for each
// outbound stream, emits SRs computed from that mapping plus the stream's
// timestamp translation (egressTiming). The NTP values therefore stay in the
// publisher's clock domain; a sender clock offset is irrelevant to receivers
// because all of one publisher's streams share it.
//
// Because the outbound NTP values are no longer the server's clock, the RTT
// pion derives from receiver reports (now - LSR - DLSR) would include the
// sender clock offset. The server instead remembers its own send time for
// each emitted SR and measures RTT from that (egressTiming.observeReception).
// Congestion control does not use SR/RR at all (it uses transport-wide CC
// feedback), so quality control is unaffected by the publisher's clock.
//
// Timing generations: an outbound stream's RTP timestamps are rewritten when
// the forwarded source changes (re-watch, codec refresh, layer switch). When
// both the old and new source mappings are known, the new offset is chosen so
// the outbound RTP->NTP mapping does not change at all (preservingOffset), so
// any SR - early or late - stays valid for every packet. When that is not
// possible the mapping changes discontinuously; the stream then enters a new
// generation. Generations are derived only from packets actually written to
// the transport, never from packets that were merely admitted, so a partially
// failed switch can never leave a generation without packets. SRs are only
// emitted for the generation of the most recently sent packet and only from
// the current source's own mapping, so a late SR of a previous source is never
// translated into the new stream. No RTP header bytes are added.
//
// Server-clock fallback: without a fresh publisher mapping (the publisher has
// sent no SR for sourceTimingMaxAge, or a different clock rate) and for
// outbound streams that are not egress-managed at all, the reporter emits
// pion's default report instead: the last sent RTP timestamp extrapolated to
// the server's wall clock. Receivers then still get a mapping for every
// stream rather than none. Switching an egress stream between the two clock
// domains after it has reported is a discontinuity, so it starts a new timing
// generation with its holdoff, exactly like a source switch.

const (
	// senderReportInterval matches pion's default SR cadence.
	senderReportInterval = time.Second
	// sourceTimingMaxAge bounds extrapolation from a publisher's last SR.
	// Browsers send audio RTCP about every 5 s; allow a few missing reports.
	sourceTimingMaxAge = 15 * time.Second
	// senderReportIdle stops SRs once nothing was sent for two intervals plus
	// slack (RFC 3550 6.4: only active senders send SRs).
	senderReportIdle = 3 * time.Second
	// timingGenerationHoldoff delays the first SR of a discontinuous new
	// generation so frames of the previous generation still buffered at the
	// receiver are played out under the mapping they were sent with.
	timingGenerationHoldoff = 300 * time.Millisecond
	// timingPreserveTolerance treats two outbound mappings as equal.
	timingPreserveTolerance = 2 * time.Millisecond
	// timingPreserveMaxStep bounds a mapping-preserving timestamp step. A
	// larger step (or a backwards one) falls back to the elapsed-time offset.
	timingPreserveMaxStep = uint32(1 << 30)
	sentReportHistory     = 16
)

// ntpDuration converts a signed difference of 64-bit NTP timestamps.
func ntpDuration(a, b uint64) time.Duration {
	diff := int64(a - b)
	seconds := diff >> 32
	fraction := uint64(diff) & 0xffffffff
	return time.Duration(seconds)*time.Second + time.Duration((fraction*uint64(time.Second))>>32)
}

// ntpAdd adds a signed duration to a 64-bit NTP timestamp.
func ntpAdd(ntp uint64, d time.Duration) uint64 {
	negative := d < 0
	if negative {
		d = -d
	}
	seconds := uint64(d / time.Second)
	fraction := (uint64(d%time.Second) << 32) / uint64(time.Second)
	delta := seconds<<32 + fraction
	if negative {
		return ntp - delta
	}
	return ntp + delta
}

// rtpTicks converts a signed duration into RTP clock ticks, rounding to the
// nearest tick so NTP<->RTP conversions round-trip exactly.
func rtpTicks(d time.Duration, clockRate uint32) int64 {
	scaled := int64(d) * int64(clockRate)
	half := int64(time.Second) / 2
	if scaled < 0 {
		return (scaled - half) / int64(time.Second)
	}
	return (scaled + half) / int64(time.Second)
}

// rtpDuration converts signed RTP ticks into a duration.
func rtpDuration(ticks int32, clockRate uint32) time.Duration {
	if clockRate == 0 {
		return 0
	}
	return time.Duration(int64(ticks) * int64(time.Second) / int64(clockRate))
}

type sourceTimingKey struct {
	publisher string
	ssrc      uint32
}

// sourceTimingMapping is one publisher SR: rtp and ntp describe the same
// capture instant on the publisher's clocks.
type sourceTimingMapping struct {
	ntp        uint64
	rtp        uint32
	clockRate  uint32
	receivedAt time.Time
}

// captureTime returns the publisher NTP time of an RTP timestamp.
func (m sourceTimingMapping) captureTime(ts uint32) uint64 {
	return ntpAdd(m.ntp, rtpDuration(int32(ts-m.rtp), m.clockRate))
}

// timestampAt returns the source RTP timestamp of a publisher NTP time.
func (m sourceTimingMapping) timestampAt(ntp uint64) uint32 {
	return m.rtp + uint32(rtpTicks(ntpDuration(ntp, m.ntp), m.clockRate))
}

type sourceTimingEntry struct {
	owner   any
	mapping sourceTimingMapping
	known   bool
}

// sourceTimingTable holds the latest publisher SR per inbound source. Only
// the inbound track currently bound to an SSRC may update it, so a late SR
// read for a replaced track cannot define the mapping of its successor. It
// has its own lock and never calls out while holding it.
type sourceTimingTable struct {
	mu      sync.Mutex
	entries map[sourceTimingKey]*sourceTimingEntry
}

func (t *sourceTimingTable) bind(publisher string, ssrc uint32, owner any) {
	if ssrc == 0 || owner == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[sourceTimingKey]*sourceTimingEntry)
	}
	t.entries[sourceTimingKey{publisher, ssrc}] = &sourceTimingEntry{owner: owner}
}

func (t *sourceTimingTable) unbind(publisher string, ssrc uint32, owner any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := sourceTimingKey{publisher, ssrc}
	if entry := t.entries[key]; entry != nil && entry.owner == owner {
		delete(t.entries, key)
	}
}

func (t *sourceTimingTable) forgetPublisher(publisher string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.entries {
		if key.publisher == publisher {
			delete(t.entries, key)
		}
	}
}

// observe records a publisher SR for its bound owner. It reports whether the
// mapping was accepted.
func (t *sourceTimingTable) observe(publisher string, owner any, report *rtcp.SenderReport, clockRate uint32, now time.Time) bool {
	if report == nil || report.NTPTime == 0 || clockRate == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[sourceTimingKey{publisher, report.SSRC}]
	if entry == nil || entry.owner != owner {
		return false
	}
	// Reordered or duplicated reports must not move the mapping backwards.
	// A report whose RTP time advanced while its NTP time did not is a
	// publisher clock step, not reordering: accept it, or the mapping would
	// go stale and reports would stop for good.
	if entry.known && int64(report.NTPTime-entry.mapping.ntp) <= 0 && int32(report.RTPTime-entry.mapping.rtp) <= 0 {
		return false
	}
	entry.mapping = sourceTimingMapping{ntp: report.NTPTime, rtp: report.RTPTime, clockRate: clockRate, receivedAt: now}
	entry.known = true
	return true
}

// mapping returns the latest fresh publisher mapping for a source.
func (t *sourceTimingTable) mapping(publisher string, ssrc uint32, now time.Time) (sourceTimingMapping, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[sourceTimingKey{publisher, ssrc}]
	if entry == nil || !entry.known {
		return sourceTimingMapping{}, false
	}
	if age := now.Sub(entry.mapping.receivedAt); age < 0 || age > sourceTimingMaxAge {
		return sourceTimingMapping{}, false
	}
	return entry.mapping, true
}

// preservingOffset returns the outbound timestamp offset (out = src + offset)
// that keeps an output's RTP->capture-time mapping unchanged when it moves
// from oldSSRC (with oldOffset) to newSSRC. A source resuming on the same
// SSRC is the same RTP stream, so its offset is kept as is.
func (t *sourceTimingTable) preservingOffset(publisher string, oldSSRC, oldOffset, newSSRC, newTimestamp uint32, now time.Time) (uint32, bool) {
	if t == nil {
		return 0, false
	}
	if oldSSRC == newSSRC {
		return oldOffset, true
	}
	previous, ok := t.mapping(publisher, oldSSRC, now)
	if !ok {
		return 0, false
	}
	next, ok := t.mapping(publisher, newSSRC, now)
	if !ok || next.clockRate != previous.clockRate {
		return 0, false
	}
	capture := next.captureTime(newTimestamp)
	out := previous.timestampAt(capture) + oldOffset
	return out - newTimestamp, true
}

// outputMappingsEqual reports whether two (source, offset) pairs describe the
// same outbound RTP->NTP mapping within tolerance.
func (t *sourceTimingTable) outputMappingsEqual(publisher string, ssrcA, offsetA, ssrcB, offsetB uint32, now time.Time) bool {
	if ssrcA == ssrcB {
		return offsetA == offsetB
	}
	a, ok := t.mapping(publisher, ssrcA, now)
	if !ok {
		return false
	}
	b, ok := t.mapping(publisher, ssrcB, now)
	if !ok || a.clockRate != b.clockRate {
		return false
	}
	// Compare the outbound timestamp each mapping assigns to one instant.
	ta := a.rtp + offsetA
	tb := b.timestampAt(a.ntp) + offsetB
	diff := rtpDuration(int32(ta-tb), a.clockRate)
	return diff <= timingPreserveTolerance && diff >= -timingPreserveTolerance
}

// preferTimingOffset prepares an output's continuity for the packet about
// to be translated: when that packet would start a new source or watch epoch,
// it records the offset that keeps the output's RTP->capture-time mapping
// (preservingOffset). switchTimestampOffset still decides whether it can be
// used. Every packet that may switch an output goes through here, the live
// fan-out and a GOP cache start alike, so the offset the SR generator later
// derives from the packets actually sent is the one chosen here.
func (r *Router) preferTimingOffset(v *videoContinuity, publisher string, pkt *rtp.Packet) {
	v.preferredOffsetOK = false
	if v.seen && (v.ssrc != pkt.SSRC || v.pendingEpoch) {
		v.preferredOffset, v.preferredOffsetOK = r.sourceTiming.preservingOffset(publisher, v.ssrc, v.timestampOffset, pkt.SSRC, pkt.Timestamp, time.Now())
	}
}

type sentSenderReport struct {
	lsr uint32 // middle 32 bits of the NTP value sent
	at  time.Time
}

// egressTiming is one outbound stream's record of what was actually sent.
// Its mutex is a leaf: it is never held across a network write and nothing
// else is acquired while holding it.
type egressTiming struct {
	mu           sync.Mutex
	seen         bool
	ssrc         uint32
	publisher    string
	sourceSSRC   uint32
	offset       uint32
	generation   uint64
	generationAt time.Time
	lastSent     time.Time
	lastRTP      uint32 // outbound timestamp of the last sent primary packet
	packets      uint32
	octets       uint32
	delivery     MediaDelivery
	guard        MediaGuard
	router       *Router
	revision     uint64
	lastNTP      uint64
	reports      [sentReportHistory]sentSenderReport
	nextReport   int
	rtt          time.Duration
	rttAt        time.Time
	// serverClock is the clock domain of this stream's reports; reported is
	// set once a report was committed in that domain.
	serverClock bool
	reported    bool
}

type egressTimingSnapshot struct {
	ssrc       uint32
	sourceSSRC uint32
	offset     uint32
	generation uint64
	packets    uint32
	octets     uint32
	lastNTP    uint64
	lastRTP    uint32
	lastSent   time.Time
	delivery   MediaDelivery
	guard      MediaGuard
	router     *Router
	revision   uint64
	publisher  string
	holdoff    bool
}

// observeSent records one primary packet that was written to the transport.
func (e *egressTiming) observeSent(header *rtp.Header, payloadSize int, ticket *mediaTicket, now time.Time) {
	if ticket == nil || ticket.timingSSRC == 0 {
		return
	}
	offset := header.Timestamp - ticket.timingTimestamp
	e.mu.Lock()
	defer e.mu.Unlock()
	e.packets++
	e.octets += uint32(payloadSize)
	e.lastSent = now
	e.lastRTP = header.Timestamp
	e.delivery, e.guard, e.router, e.revision = ticket.delivery, ticket.guard, ticket.router, ticket.videoRevision
	if e.seen && e.ssrc == header.SSRC && e.publisher == ticket.delivery.SenderID &&
		e.sourceSSRC == ticket.timingSSRC && e.offset == offset {
		return
	}
	continuous := false
	if e.seen && e.ssrc == header.SSRC && e.publisher == ticket.delivery.SenderID && ticket.router != nil {
		continuous = ticket.router.sourceTiming.outputMappingsEqual(e.publisher, e.sourceSSRC, e.offset, ticket.timingSSRC, offset, now)
	}
	e.ssrc, e.publisher, e.sourceSSRC, e.offset = header.SSRC, ticket.delivery.SenderID, ticket.timingSSRC, offset
	if !continuous {
		// A fresh generation on every discontinuity, never a reused number.
		e.generation++
		e.generationAt = now
	}
	e.seen = true
}

func (e *egressTiming) snapshot(now time.Time) (egressTimingSnapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.seen || now.Sub(e.lastSent) > senderReportIdle {
		return egressTimingSnapshot{}, false
	}
	return egressTimingSnapshot{
		ssrc: e.ssrc, sourceSSRC: e.sourceSSRC, offset: e.offset, generation: e.generation,
		packets: e.packets, octets: e.octets, lastNTP: e.lastNTP, lastRTP: e.lastRTP, lastSent: e.lastSent,
		delivery: e.delivery, guard: e.guard, router: e.router, revision: e.revision, publisher: e.publisher,
		holdoff: e.generation > 1 && now.Sub(e.generationAt) < timingGenerationHoldoff,
	}, true
}

// commitReport rechecks that the stream is still in the generation the
// report was computed for and records its send time for RTT measurement.
func (e *egressTiming) commitReport(report *rtcp.SenderReport, generation uint64, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.seen || e.generation != generation || e.ssrc != report.SSRC {
		return false
	}
	// Extrapolation from a consistent mapping always advances; a repeated
	// value adds nothing. A publisher clock step is passed through rather
	// than suppressing reports until the old time is reached again.
	if report.NTPTime == e.lastNTP {
		return false
	}
	e.lastNTP = report.NTPTime
	e.reported = true
	e.reports[e.nextReport] = sentSenderReport{lsr: uint32(report.NTPTime >> 16), at: now}
	e.nextReport = (e.nextReport + 1) % sentReportHistory
	return true
}

// observeReception measures RTT from a receiver's reception report against
// this server's own send time of the referenced SR, independent of the
// publisher clock carried in the SR.
func (e *egressTiming) observeReception(report rtcp.ReceptionReport, now time.Time) {
	if report.LastSenderReport == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, sent := range e.reports {
		if sent.lsr != report.LastSenderReport || sent.at.IsZero() {
			continue
		}
		delay := time.Duration(uint64(report.Delay) * uint64(time.Second) >> 16)
		rtt := now.Sub(sent.at) - delay
		if rtt < 0 {
			rtt = 0
		}
		e.rtt, e.rttAt = rtt, now
		return
	}
}

// selectClock records the clock domain of the next report. Leaving a domain
// this stream has already reported in starts a new generation (with its
// holdoff); it returns the generation to report in, or false while held off.
func (e *egressTiming) selectClock(serverClock bool, now time.Time) (uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.seen {
		return 0, false
	}
	if e.serverClock != serverClock {
		e.serverClock = serverClock
		if e.reported {
			e.reported = false
			e.generation++
			e.generationAt = now
		}
	}
	if e.generation > 1 && now.Sub(e.generationAt) < timingGenerationHoldoff {
		return 0, false
	}
	return e.generation, true
}

func (e *egressTiming) roundTrip() (time.Duration, time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rtt, e.rttAt
}

// timingAuthorized rechecks, without holding any forwarding lock across the
// later network write, that the stream may still carry the source.
func (r *Router) timingAuthorized(snapshot egressTimingSnapshot) bool {
	delivery := snapshot.delivery
	if delivery.sourceLive != nil {
		if !delivery.sourceLive.Load() {
			return false
		}
	} else if !delivery.Tap {
		r.mu.RLock()
		claim, ok := r.slotClaims[delivery.SenderID][delivery.Slot]
		current := ok && claim.token == delivery.SourceEpoch && claim.live != nil && claim.live.Load()
		gate := r.canTalk
		music := r.isMusicChannelLocked(delivery.SenderID)
		r.mu.RUnlock()
		if !current {
			return false
		}
		if delivery.Slot == SlotMic && gate != nil && !music && !gate(delivery.SenderID) {
			return false
		}
	}
	allowed := false
	_ = r.mediaCommit(delivery, snapshot.guard, snapshot.revision)(func() error {
		allowed = true
		return nil
	})
	return allowed
}

// senderReport computes this stream's next SR from the publisher mapping.
// It returns the report and the generation it belongs to.
func (s *mediaEgressStream) senderReport(clockRate uint32, now time.Time) (*rtcp.SenderReport, uint64, bool) {
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if !active {
		return nil, 0, false
	}
	snapshot, ok := s.timing.snapshot(now)
	if !ok || snapshot.holdoff || snapshot.router == nil {
		return nil, 0, false
	}
	if !snapshot.router.timingAuthorized(snapshot) {
		return nil, 0, false
	}
	mapping, fresh := snapshot.router.sourceTiming.mapping(snapshot.publisher, snapshot.sourceSSRC, now)
	fresh = fresh && mapping.clockRate == clockRate
	generation, ok := s.timing.selectClock(!fresh, now)
	if !ok {
		return nil, 0, false
	}
	if !fresh {
		return serverClockReport(snapshot.ssrc, snapshot.lastRTP, snapshot.lastSent, clockRate, snapshot.packets, snapshot.octets, now), generation, true
	}
	elapsed := now.Sub(mapping.receivedAt)
	report := &rtcp.SenderReport{
		SSRC:        snapshot.ssrc,
		NTPTime:     ntpAdd(mapping.ntp, elapsed),
		RTPTime:     mapping.rtp + snapshot.offset + uint32(rtpTicks(elapsed, clockRate)),
		PacketCount: snapshot.packets,
		OctetCount:  snapshot.octets,
	}
	return report, generation, true
}

// ntpEpochOffset is the number of seconds from 1900 (NTP) to 1970 (Unix).
const ntpEpochOffset = 2208988800

// serverNTP returns the server's wall clock as an NTP timestamp.
func serverNTP(now time.Time) uint64 {
	return ntpAdd(uint64(ntpEpochOffset)<<32, time.Duration(now.UnixNano()))
}

// serverClockReport is pion's default SR: the last sent RTP timestamp,
// extrapolated to now, against the server's wall clock.
func serverClockReport(ssrc, lastRTP uint32, lastSent time.Time, clockRate, packets, octets uint32, now time.Time) *rtcp.SenderReport {
	return &rtcp.SenderReport{
		SSRC:        ssrc,
		NTPTime:     serverNTP(now),
		RTPTime:     lastRTP + uint32(rtpTicks(now.Sub(lastSent), clockRate)),
		PacketCount: packets,
		OctetCount:  octets,
	}
}

// localSendStats records what an outbound stream that is not egress-managed
// actually sent, for its server-clock SRs. Its mutex is a leaf.
type localSendStats struct {
	mu       sync.Mutex
	seen     bool
	lastRTP  uint32
	lastSent time.Time
	packets  uint32
	octets   uint32
}

func (l *localSendStats) observe(header *rtp.Header, payloadSize int, now time.Time) {
	l.mu.Lock()
	l.seen = true
	l.lastRTP, l.lastSent = header.Timestamp, now
	l.packets++
	l.octets += uint32(payloadSize)
	l.mu.Unlock()
}

func (l *localSendStats) report(ssrc, clockRate uint32, now time.Time) (*rtcp.SenderReport, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.seen || clockRate == 0 || now.Sub(l.lastSent) > senderReportIdle {
		return nil, false
	}
	return serverClockReport(ssrc, l.lastRTP, l.lastSent, clockRate, l.packets, l.octets, now), true
}

func (r *mediaEgressRegistry) stream(id string) *mediaEgressStream {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.streams[id]
}

// roundTrip returns the RTT measured from SRs sent on an outbound SSRC.
func (r *mediaEgressRegistry) roundTrip(ssrc uint32) (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	streams := make([]*mediaEgressStream, 0, len(r.streams))
	for _, s := range r.streams {
		streams = append(streams, s)
	}
	r.mu.Unlock()
	for _, s := range streams {
		if s.outputSSRC.Load() != ssrc {
			continue
		}
		rtt, at := s.timing.roundTrip()
		if !at.IsZero() && time.Since(at) <= sourceTimingMaxAge {
			return rtt, true
		}
	}
	return 0, false
}

// sourceTimedReportFactory replaces pion's SR generator for egress-managed
// outbound streams (see the package comment above).
type sourceTimedReportFactory struct {
	registry *mediaEgressRegistry
	interval time.Duration
	now      func() time.Time
}

func (f sourceTimedReportFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	interval := f.interval
	if interval <= 0 {
		interval = senderReportInterval
	}
	now := f.now
	if now == nil {
		now = time.Now
	}
	return &sourceTimedReporter{registry: f.registry, interval: interval, now: now, streams: make(map[uint32]sourceTimedStream), close: make(chan struct{})}, nil
}

type sourceTimedStream struct {
	id        string
	ssrc      uint32
	clockRate uint32
	local     *localSendStats
}

type sourceTimedReporter struct {
	interceptor.NoOp
	registry *mediaEgressRegistry
	interval time.Duration
	now      func() time.Time
	mu       sync.Mutex
	streams  map[uint32]sourceTimedStream
	started  bool
	closed   bool
	close    chan struct{}
	wg       sync.WaitGroup
}

func (s *sourceTimedReporter) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.started {
		return writer
	}
	s.started = true
	s.wg.Add(1)
	go s.loop(writer)
	return writer
}

func (s *sourceTimedReporter) BindRTCPReader(reader interceptor.RTCPReader) interceptor.RTCPReader {
	return interceptor.RTCPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attrs, err := reader.Read(b, a)
		if err != nil {
			return n, attrs, err
		}
		if attrs == nil {
			attrs = make(interceptor.Attributes)
		}
		packets, perr := attrs.GetRTCPPackets(b[:n])
		if perr != nil {
			return n, attrs, err
		}
		now := s.now()
		for _, packet := range packets {
			var reports []rtcp.ReceptionReport
			switch p := packet.(type) {
			case *rtcp.ReceiverReport:
				reports = p.Reports
			case *rtcp.SenderReport:
				reports = p.Reports
			}
			for _, report := range reports {
				if stream := s.streamFor(report.SSRC); stream != nil {
					stream.timing.observeReception(report, now)
				}
			}
		}
		return n, attrs, err
	})
}

func (s *sourceTimedReporter) streamFor(ssrc uint32) *mediaEgressStream {
	s.mu.Lock()
	info, ok := s.streams[ssrc]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.registry.stream(info.id)
}

func (s *sourceTimedReporter) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	local := &localSendStats{}
	s.mu.Lock()
	s.streams[info.SSRC] = sourceTimedStream{id: info.ID, ssrc: info.SSRC, clockRate: info.ClockRate, local: local}
	s.mu.Unlock()
	ssrc := info.SSRC
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		// Only streams the egress registry does not manage report from this;
		// recording is a leaf-locked counter update, like pion's own SR
		// interceptor.
		if header.SSRC == ssrc {
			local.observe(header, len(payload), s.now())
		}
		return writer.Write(header, payload, attrs)
	})
}

func (s *sourceTimedReporter) UnbindLocalStream(info *interceptor.StreamInfo) {
	s.mu.Lock()
	if current, ok := s.streams[info.SSRC]; ok && current.id == info.ID {
		delete(s.streams, info.SSRC)
	}
	s.mu.Unlock()
}

func (s *sourceTimedReporter) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.close)
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *sourceTimedReporter) loop(writer interceptor.RTCPWriter) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.writeReports(writer)
		case <-s.close:
			return
		}
	}
}

// writeReports snapshots streams, then computes and writes each SR with no
// lock held across the network write. A slow write delays only this peer's
// reports, never media forwarding.
func (s *sourceTimedReporter) writeReports(writer interceptor.RTCPWriter) {
	s.mu.Lock()
	infos := make([]sourceTimedStream, 0, len(s.streams))
	for _, info := range s.streams {
		infos = append(infos, info)
	}
	s.mu.Unlock()
	for _, info := range infos {
		now := s.now()
		stream := s.registry.stream(info.id)
		if stream == nil {
			// Not egress-managed: pion's default server-clock report.
			if report, ok := info.local.report(info.ssrc, info.clockRate, now); ok {
				_, _ = writer.Write([]rtcp.Packet{report}, interceptor.Attributes{})
			}
			continue
		}
		report, generation, ok := stream.senderReport(info.clockRate, now)
		if !ok || !stream.timing.commitReport(report, generation, now) {
			continue
		}
		_, _ = writer.Write([]rtcp.Packet{report}, interceptor.Attributes{})
	}
}

// readSourceTiming drains one inbound track's RTCP. Reading is what runs the
// receive-side interceptors (receiver reports then carry LSR/DLSR), and the
// publisher's SRs define the capture-time mapping forwarded to receivers.
func (r *Router) readSourceTiming(publisherID string, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	if track == nil || receiver == nil {
		return
	}
	ssrc := uint32(track.SSRC())
	r.sourceTiming.bind(publisherID, ssrc, track)
	defer r.sourceTiming.unbind(publisherID, ssrc, track)
	rid := track.RID()
	buf := make([]byte, 1500)
	for {
		n, _, err := receiver.ReadSimulcast(buf, rid)
		if err != nil {
			return
		}
		packets, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			continue // one malformed compound packet must not stop the drain
		}
		now := time.Now()
		for _, packet := range packets {
			if report, ok := packet.(*rtcp.SenderReport); ok && report.SSRC == ssrc {
				r.sourceTiming.observe(publisherID, track, report, track.Codec().ClockRate, now)
			}
		}
	}
}
