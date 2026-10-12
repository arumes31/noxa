package webrtc

import (
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"noxa/internal/netproto"
)

const upstreamFeedbackSlots = 16

// A single construction handoff, never a per-packet or unbounded peer map.
// Engine.mu serializes API construction; alternate API users get a no-op.
// The owning arm must always be disarmed, including failed Pion constructors.
type upstreamFeedbackRegistry struct {
	mu        sync.Mutex
	lease     *upstreamFeedbackLease
	pendingID string
	pending   *upstreamFeedbackObserver
}
type upstreamFeedbackLease struct{ invalid bool }

func (r *upstreamFeedbackRegistry) arm() *upstreamFeedbackLease {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease != nil {
		r.lease.invalid = true
		r.pending, r.pendingID = nil, ""
		return nil // Reentrant construction makes attribution unknown, not transport invalid.
	}
	r.lease = &upstreamFeedbackLease{}
	return r.lease
}
func (r *upstreamFeedbackRegistry) disarm(lease *upstreamFeedbackLease) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lease != nil && r.lease == lease {
		r.lease, r.pending, r.pendingID = nil, nil, ""
	}
}
func (r *upstreamFeedbackRegistry) take(lease *upstreamFeedbackLease, id string) *upstreamFeedbackObserver {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lease == nil || r.lease != lease || lease.invalid || r.pendingID != id {
		return nil
	}
	observer := r.pending
	r.pending, r.pendingID = nil, ""
	return observer
}
func (r *upstreamFeedbackRegistry) remove(observer *upstreamFeedbackObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == observer {
		r.pending, r.pendingID = nil, ""
	}
}

type upstreamFeedbackFactory struct{ registry *upstreamFeedbackRegistry }

func (f upstreamFeedbackFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	if f.registry == nil {
		return &interceptor.NoOp{}, nil
	}
	r := f.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil || r.lease.invalid {
		return &interceptor.NoOp{}, nil
	}
	if r.pending != nil {
		r.lease.invalid = true
		r.pending, r.pendingID = nil, ""
		return &interceptor.NoOp{}, nil
	}
	observer := &upstreamFeedbackObserver{registry: r, started: time.Now()}
	r.pending, r.pendingID = observer, id
	return observer, nil
}

// Only outgoing compound writes containing TransportLayerCC are observed.
// Locking covers counter updates, never the wrapped writer. Sixteen slots
// retain start times; additional concurrent writes are counted without storage.
type upstreamFeedbackObserver struct {
	interceptor.NoOp
	registry    *upstreamFeedbackRegistry
	mu          sync.Mutex
	started     time.Time
	closed      bool
	counters    netproto.VideoUpstreamFeedbackDiagnostics
	readTiming  upstreamReadTimingObserver
	lastAttempt time.Time
	lastGap     int64
	slots       [upstreamFeedbackSlots]time.Time
	untracked   uint64
}

func (o *upstreamFeedbackObserver) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	return interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
		var reports, statuses uint64
		for _, packet := range packets {
			if feedback, ok := packet.(*rtcp.TransportLayerCC); ok && feedback != nil {
				reports++
				statuses += uint64(feedback.PacketStatusCount)
			}
		}
		if reports == 0 {
			return writer.Write(packets, attrs)
		}
		slot := o.begin(reports, statuses)
		if slot == -2 {
			return writer.Write(packets, attrs)
		}
		started := time.Now()
		n, err := writer.Write(packets, attrs)
		o.finish(slot, time.Since(started), err != nil)
		return n, err
	})
}
func (o *upstreamFeedbackObserver) begin(reports, statuses uint64) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return -2
	}
	// Timestamp serialized observation entry, so concurrent callers cannot reverse gaps.
	now := time.Now()
	d := &o.counters
	d.Attempts++
	d.Reports += reports
	d.ReportedPackets += statuses
	if !o.lastAttempt.IsZero() {
		gap := max(int64(0), now.Sub(o.lastAttempt).Microseconds())
		o.lastGap = gap
		d.MaxAttemptGapUS = max(d.MaxAttemptGapUS, uint64(gap))
	}
	o.lastAttempt = now
	d.InFlight++
	for i := range o.slots {
		if o.slots[i].IsZero() {
			o.slots[i] = now
			return i
		}
	}
	o.untracked++
	d.InFlightOverflow++
	return -1
}
func (o *upstreamFeedbackObserver) finish(slot int, elapsed time.Duration, failed bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d := &o.counters
	d.InFlight--
	if slot >= 0 {
		o.slots[slot] = time.Time{}
	} else {
		o.untracked--
	}
	d.CompletedWrites++
	if failed {
		d.WriteErrors++
	}
	micros := uint64(max(int64(0), elapsed.Microseconds()))
	d.DurationTotalUS += micros
	d.DurationMaxUS = max(d.DurationMaxUS, micros)
	bucket := 7
	for i, bound := range [...]uint64{100, 500, 1000, 5000, 20000, 100000, 500000} {
		if micros <= bound {
			bucket = i
			break
		}
	}
	d.DurationHistogram[bucket]++
}
func (o *upstreamFeedbackObserver) snapshot(now time.Time) *netproto.VideoUpstreamFeedbackDiagnostics {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.counters
	result.StartedAt = o.started.UnixMilli()
	result.SampleMS = max(int64(0), now.Sub(o.started).Milliseconds())
	result.Closed = o.closed
	if result.Attempts > 1 {
		gap := o.lastGap
		result.LastAttemptGapUS = &gap
	}
	if !o.lastAttempt.IsZero() {
		age := max(int64(0), now.Sub(o.lastAttempt).Microseconds())
		result.LastAttemptAgeUS = &age
	}
	result.InFlightAgeUnknown = o.untracked != 0
	if result.InFlight != 0 && !result.InFlightAgeUnknown {
		oldest := now
		for _, started := range o.slots {
			if !started.IsZero() && started.Before(oldest) {
				oldest = started
			}
		}
		age := max(int64(0), now.Sub(oldest).Microseconds())
		result.InFlightOldestAgeUS = &age
	}
	return &result
}
func (o *upstreamFeedbackObserver) Close() error {
	o.mu.Lock()
	o.closed = true
	o.readTiming.closed.Store(true)
	o.mu.Unlock()
	if o.registry != nil {
		o.registry.remove(o)
	}
	return nil
}
