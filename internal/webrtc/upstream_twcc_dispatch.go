package webrtc

import (
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"noxa/internal/netproto"
)

const upstreamTWCCDispatchBatches = 4
const upstreamTWCCDispatchBytes = 64 * 1024
const upstreamTWCCDispatchAge = 250 * time.Millisecond

var errUpstreamTWCCDispatchClosed = errors.New("upstream TWCC dispatcher closed")
var errUpstreamTWCCDispatchInvalid = errors.New("invalid upstream TWCC batch")

type upstreamTWCCBatch struct {
	packets []rtcp.Packet
	bytes   int
	at      time.Time
}

// Only the default recorder's pure TWCC batches with nil attributes are queued.
// Arbitrary attributes and compound/non-TWCC feedback retain their synchronous
// semantics. The recorder's transport-wide timestamps and counters are untouched.
type upstreamTWCCDispatch struct {
	interceptor.NoOp
	mu       sync.Mutex
	writer   interceptor.RTCPWriter
	queue    []upstreamTWCCBatch
	bytes    int
	closed   bool
	counters netproto.VideoUpstreamTWCCDispatchDiagnostics
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
}

func newUpstreamTWCCDispatch() *upstreamTWCCDispatch {
	d := &upstreamTWCCDispatch{queue: make([]upstreamTWCCBatch, 0, upstreamTWCCDispatchBatches), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	return d
}

func (d *upstreamTWCCDispatch) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	d.mu.Lock()
	// Pion binds once per PC. Refuse a replacement rather than retarget queued
	// reports to a different writer or launch another worker.
	if d.writer != nil || d.closed {
		d.mu.Unlock()
		return interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) { return 0, errUpstreamTWCCDispatchClosed })
	}
	d.writer = writer
	go d.run()
	d.mu.Unlock()
	return interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
		d.mu.Lock()
		if d.closed {
			d.counters.DroppedClosed++
			d.mu.Unlock()
			return 0, errUpstreamTWCCDispatchClosed
		}
		d.mu.Unlock()
		pure := len(packets) > 0 && attrs == nil
		for _, packet := range packets {
			feedback, ok := packet.(*rtcp.TransportLayerCC)
			if !ok || feedback == nil {
				pure = false
				break
			}
		}
		if !pure {
			return writer.Write(packets, attrs)
		}
		// Bound before serialization/allocation; Marshal may reject malformed input.
		size := 0
		for _, packet := range packets {
			n := packet.MarshalSize()
			if n <= 0 || n > upstreamTWCCDispatchBytes-size {
				d.mu.Lock()
				d.counters.RejectedOversize++
				d.mu.Unlock()
				return 0, errUpstreamTWCCDispatchInvalid
			}
			size += n
		}
		raw, err := rtcp.Marshal(packets)
		if err != nil || len(raw) > upstreamTWCCDispatchBytes {
			d.mu.Lock()
			d.counters.RejectedInvalid++
			d.mu.Unlock()
			return 0, errUpstreamTWCCDispatchInvalid
		}
		owned, err := rtcp.Unmarshal(raw)
		if err != nil || len(owned) != len(packets) {
			d.mu.Lock()
			d.counters.RejectedInvalid++
			d.mu.Unlock()
			return 0, errUpstreamTWCCDispatchInvalid
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closed {
			d.counters.DroppedClosed++
			return 0, errUpstreamTWCCDispatchClosed
		}
		now := time.Now()
		d.expireLocked(now)
		for len(d.queue) >= upstreamTWCCDispatchBatches || d.bytes+len(raw) > upstreamTWCCDispatchBytes {
			d.popLocked()
			d.counters.DroppedOverflow++
		}
		d.queue = append(d.queue, upstreamTWCCBatch{packets: owned, bytes: len(raw), at: now})
		d.bytes += len(raw)
		d.counters.Accepted++
		select {
		case d.wake <- struct{}{}:
		default:
		}
		// This reports admission, not delivery. Written/WriteErrors count the real
		// underlying completion separately; failed reports are never retried.
		return len(raw), nil
	})
}

func (d *upstreamTWCCDispatch) popLocked() upstreamTWCCBatch {
	batch := d.queue[0]
	copy(d.queue, d.queue[1:])
	d.queue[len(d.queue)-1] = upstreamTWCCBatch{}
	d.queue = d.queue[:len(d.queue)-1]
	d.bytes -= batch.bytes
	return batch
}

// Pending reports are aged out before admission or dispatch. While a writer is
// blocked, memory stays bounded; this is not a physical eviction deadline.
func (d *upstreamTWCCDispatch) expireLocked(now time.Time) {
	for len(d.queue) > 0 && now.Sub(d.queue[0].at) >= upstreamTWCCDispatchAge {
		d.popLocked()
		d.counters.DroppedExpired++
	}
}
func (d *upstreamTWCCDispatch) run() {
	defer close(d.done)
	for {
		select {
		case <-d.stop:
			return
		case <-d.wake:
		}
		for {
			d.mu.Lock()
			if d.closed {
				d.mu.Unlock()
				return
			}
			d.expireLocked(time.Now())
			if len(d.queue) == 0 {
				d.mu.Unlock()
				break
			}
			batch, writer := d.popLocked(), d.writer
			d.mu.Unlock()
			_, err := writer.Write(batch.packets, nil)
			d.mu.Lock()
			d.counters.Written++
			if err != nil {
				d.counters.WriteErrors++
			}
			d.mu.Unlock()
		}
	}
}

func (d *upstreamTWCCDispatch) snapshot(now time.Time) *netproto.VideoUpstreamTWCCDispatchDiagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.counters
	result.Closed, result.QueuedBatches, result.QueuedBytes = d.closed, len(d.queue), d.bytes
	if len(d.queue) > 0 {
		age := max(int64(0), now.Sub(d.queue[0].at).Milliseconds())
		result.OldestAgeMS = &age
	}
	return &result
}

// An in-flight transport write cannot be cancelled by the interceptor API.
// Pion stops DTLS before closing its interceptor chain, unblocking that write.
// Standalone callers must likewise release/close their writer before waiting.
func (d *upstreamTWCCDispatch) Close() error {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.counters.DroppedClosed += uint64(len(d.queue))
		clear(d.queue)
		d.queue = nil
		d.bytes = 0
		close(d.stop)
		if d.writer == nil {
			close(d.done)
		}
	}
	d.mu.Unlock()
	<-d.done
	return nil
}
