package webrtc

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"noxa/internal/netproto"
)

// Match the existing repaired-burst capacity, not its residence time. These
// are retained RTP wire-byte/packet bounds; bounded Go headers are additional.
// A stalled consumer never backpressures Pion's receive/TWCC path. Expired
// packets are discarded before processing, not sent as a delayed backlog.
const videoDispatchPackets = videoReorderPackets
const videoDispatchBytes = videoReorderBytes
const videoDispatchLifetime = 250 * time.Millisecond

type videoDispatchPacket struct {
	packet        *rtp.Packet
	codec         pion.RTPCodecCapability
	at            time.Time
	size          int
	discontinuity bool
}

type videoDispatchLimits struct {
	packets, bytes int
	lifetime       time.Duration
}

type videoIngressDispatch struct {
	overflow, expired, cancelledPackets, recoveryDrops, discontinuities uint64
	maxWait                                                             time.Duration
	activeAt                                                            time.Time
	maxCallback                                                         time.Duration
	mu                                                                  sync.Mutex
	queue                                                               []videoDispatchPacket
	head, count, bytes                                                  int
	closed, gap                                                         bool
	dropped                                                             uint64
	cancelled                                                           atomic.Bool
	wake, done                                                          chan struct{}
	limits                                                              videoDispatchLimits
	now                                                                 func() time.Time
	current                                                             func() bool
	emit                                                                func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool)
}

func newVideoIngressDispatch(current func() bool, emit func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool)) *videoIngressDispatch {
	return newVideoIngressDispatchWithLimits(current, emit, time.Now, videoDispatchLimits{videoDispatchPackets, videoDispatchBytes, videoDispatchLifetime})
}

func newVideoIngressDispatchWithLimits(current func() bool, emit func(*rtp.Packet, pion.RTPCodecCapability, time.Time, bool), now func() time.Time, limits videoDispatchLimits) *videoIngressDispatch {
	d := &videoIngressDispatch{current: current, emit: emit, now: now, limits: limits, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go d.run()
	return d
}

// One producer is the owned reorder loop. Copy before returning because even
// contiguous packets and negotiated feedback slices may be reused next read.
// No consumer callback, router lock or network operation runs under mu.
func (d *videoIngressDispatch) enqueue(packet *rtp.Packet, codec pion.RTPCodecCapability) bool {
	if packet == nil {
		return false
	}
	size := packet.MarshalSize()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.cancelled.Load() {
		return false
	}
	if size > 65535 || d.count >= d.limits.packets || size > d.limits.bytes-d.bytes {
		d.gap = true
		d.dropped++
		d.overflow++
		return false
	}
	if d.queue == nil {
		d.queue = make([]videoDispatchPacket, d.limits.packets)
	}
	codec.RTCPFeedback = slices.Clone(codec.RTCPFeedback)
	d.queue[(d.head+d.count)%len(d.queue)] = videoDispatchPacket{packet.Clone(), codec, d.now(), size, d.gap}
	d.gap = false
	d.count++
	d.bytes += size
	d.signal()
	return true
}

func (d *videoIngressDispatch) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// cancel invalidates queued work immediately. An already-entered callback
// still belongs to this worker; callers must join it, never detach it. Existing
// media callbacks must return; Go cannot forcibly cancel an arbitrary writer.
func (d *videoIngressDispatch) cancel() {
	d.cancelled.Store(true)
	d.mu.Lock()
	d.closed = true
	d.cancelledPackets += uint64(d.count)
	d.queue = nil
	d.head, d.count, d.bytes = 0, 0, 0
	d.mu.Unlock()
	d.signal()
}

func (d *videoIngressDispatch) finish(drain bool) {
	if !drain {
		d.cancel()
	} else {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		d.signal()
	}
	<-d.done
}

func (d *videoIngressDispatch) run() {
	defer func() { d.mu.Lock(); d.activeAt = time.Time{}; d.mu.Unlock(); close(d.done) }()
	lost := false
	for {
		d.mu.Lock()
		if d.count == 0 {
			closed := d.closed
			d.mu.Unlock()
			if closed {
				return
			}
			<-d.wake
			continue
		}
		item := d.queue[d.head]
		d.queue[d.head] = videoDispatchPacket{}
		d.head = (d.head + 1) % len(d.queue)
		d.count--
		d.bytes -= item.size
		d.activeAt = d.now()
		d.mu.Unlock()
		if d.cancelled.Load() || !d.current() {
			d.cancel()
			return
		}
		lost = lost || item.discontinuity
		// Recheck after the source guard: contention there must not turn a
		// stale envelope into a newly admitted callback.
		d.mu.Lock()
		wait := max(time.Duration(0), d.now().Sub(item.at))
		d.maxWait = max(d.maxWait, wait)
		if wait > d.limits.lifetime {
			d.expired++
			d.dropped++
			d.activeAt = time.Time{}
			d.mu.Unlock()
			lost = true
			continue
		}
		if lost {
			d.discontinuities++
		}
		d.mu.Unlock()
		d.emit(item.packet, item.codec, item.at, lost)
		d.mu.Lock()
		d.maxCallback = max(d.maxCallback, d.now().Sub(d.activeAt))
		d.activeAt = time.Time{}
		d.mu.Unlock()
		lost = false
	}
}

// Snapshot on request only; never acquire the ingress diagnostic mutex in the
// reader. Age starts at reorder emission, not kernel or ReadRTP arrival. The
// admission lifetime does not interrupt an active callback/access-unit fanout.
func (d *videoIngressDispatch) snapshot(now time.Time) *netproto.VideoDispatchDiagnostics {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := &netproto.VideoDispatchDiagnostics{QueuedPackets: d.count, QueuedBytes: d.bytes, MaxWaitMS: d.maxWait.Milliseconds(), OverflowDrops: d.overflow, ExpiryDrops: d.expired, CancelledPackets: d.cancelledPackets, RecoveryDrops: d.recoveryDrops, Discontinuities: d.discontinuities, MaxCallbackMS: d.maxCallback.Milliseconds()}
	if d.count > 0 {
		out.OldestQueuedMS = max(0, now.Sub(d.queue[d.head].at).Milliseconds())
	}
	if !d.activeAt.IsZero() {
		out.CallbackActive = true
		out.CallbackAgeMS = max(0, now.Sub(d.activeAt).Milliseconds())
	}
	return out
}
func (d *videoIngressDispatch) discardRecovery(n int) {
	d.mu.Lock()
	d.recoveryDrops += uint64(n)
	d.mu.Unlock()
}
