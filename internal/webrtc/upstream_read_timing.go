package webrtc

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"noxa/internal/netproto"
	"noxa/internal/safecast"
)

const upstreamReadSampleEvery = 64

type upstreamReadTimingKey struct{}

// One token per sampled invocation, never a shared per-stream last timestamp.
// The caller-owned attribute map is copied before adding this private key. The
// inner reader only mutates the owned token; cleanup also copies returned attrs.
// State 0=not observed,1=being stamped,2=stamped,3=invalid/repeated observation.
type upstreamReadTimingToken struct {
	owner *upstreamReadTimingObserver
	state atomic.Uint32
	at    time.Time
}

type upstreamReadTimingObserver struct {
	closed   atomic.Bool
	calls    atomic.Uint64
	sampled  atomic.Uint64
	mu       sync.Mutex
	counters netproto.VideoUpstreamReadTimingDiagnostics
}

// Already registered before defaults for RTCP observation. This inner edge is
// immediately outside the SRTP reader. No read/queue/pacing semantics change.
func (o *upstreamFeedbackObserver) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	extensionID := timingExtensionID(info)
	if capture := o.timing.Load(); capture != nil && info != nil && info.SSRC == capture.scope.SSRC {
		capture.stop("stream_rebound", time.Now())
	}
	return interceptor.RTPReaderFunc(func(buf []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, result, err := reader.Read(buf, attrs)
		if capture := o.timing.Load(); capture != nil && info != nil && info.SSRC == capture.scope.SSRC {
			at := time.Now()
			switch {
			case err != nil:
				var timeout net.Error
				if errors.As(err, &timeout) && timeout.Timeout() {
					capture.readTimeouts.Add(1)
				} else {
					capture.stop("read_error", at)
				}
			case n > 0 && n <= len(buf):
				var header rtp.Header
				if _, parseErr := header.Unmarshal(buf[:n]); parseErr != nil {
					capture.stop("invalid_rtp_header", at)
				} else {
					capture.recordRTP(header, extensionID, at)
				}
			default:
				capture.stop("invalid_read_length", at)
			}
			capture.observeRTPHook(at)
		}
		if token, ok := attrs[upstreamReadTimingKey{}].(*upstreamReadTimingToken); ok && token != nil && token.owner == &o.readTiming {
			if !token.state.CompareAndSwap(0, 1) {
				token.state.Store(3)
			} else {
				token.at = time.Now()
				token.state.CompareAndSwap(1, 2)
			}
		}
		return n, result, err
	})
}

// Factory uses the existing Engine.mu-serialized one-construction handoff.
// It never creates a peer map or retains an observer outside the owning PC.
// Registration after defaults makes this the outer receive edge. Defaults'
// last receive interceptor is TWCC; the existing inner edge precedes them all.
type upstreamReadTimingOuterFactory struct{ registry *upstreamFeedbackRegistry }

func (f upstreamReadTimingOuterFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	if f.registry == nil {
		return &interceptor.NoOp{}, nil
	}
	r := f.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil || r.lease.invalid || r.pendingID != id || r.pending == nil {
		return &interceptor.NoOp{}, nil
	}
	return &upstreamReadTimingOuter{observer: &r.pending.readTiming}, nil
}

type upstreamReadTimingOuter struct {
	interceptor.NoOp
	observer *upstreamReadTimingObserver
}

func (o *upstreamReadTimingOuter) BindRemoteStream(_ *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	return interceptor.RTPReaderFunc(func(buf []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		d := o.observer
		if d.closed.Load() {
			return reader.Read(buf, attrs)
		}
		if d.calls.Add(1)%upstreamReadSampleEvery != 0 {
			return reader.Read(buf, attrs)
		}
		d.sampled.Add(1)
		if _, collision := attrs[upstreamReadTimingKey{}]; collision {
			d.record(nil, time.Time{}, false, true)
			return reader.Read(buf, attrs)
		}
		token := &upstreamReadTimingToken{owner: d}
		owned := make(interceptor.Attributes, len(attrs)+1)
		for key, value := range attrs {
			owned[key] = value
		}
		owned[upstreamReadTimingKey{}] = token
		n, result, err := reader.Read(buf, owned)
		ended := time.Now()
		d.record(token, ended, err != nil, false)
		// Preserve nil/error results and all unrelated attributes, never mutate
		// an attribute map which an interceptor or caller may retain.
		if value, exists := result[upstreamReadTimingKey{}]; exists && value == token {
			clean := make(interceptor.Attributes, len(result)-1)
			for key, value := range result {
				if key != (upstreamReadTimingKey{}) {
					clean[key] = value
				}
			}
			result = clean
		}
		return n, result, err
	})
}

func (d *upstreamReadTimingObserver) record(token *upstreamReadTimingToken, ended time.Time, readError, collision bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed.Load() {
		return
	}
	c := &d.counters
	if collision {
		c.InvalidToken++
		return
	}
	if readError {
		c.ReadErrors++
		return
	}
	if token != nil && token.owner != d {
		c.InvalidToken++
		return
	}
	if token == nil || token.state.Load() != 2 {
		if token == nil || token.state.Load() == 0 {
			c.MissingInner++
		} else {
			c.InvalidToken++
		}
		return
	}
	if token.at.IsZero() || ended.Before(token.at) {
		c.InvalidToken++
		return
	}
	micros, err := safecast.Int64ToUint64(ended.Sub(token.at).Microseconds())
	if err != nil {
		c.InvalidToken++
		return
	}
	c.CompletedSamples++
	c.DurationTotalUS += micros
	c.DurationMaxUS = max(c.DurationMaxUS, micros)
	bucket := 7
	for i, bound := range [...]uint64{100, 500, 1000, 5000, 20000, 100000, 500000} {
		if micros <= bound {
			bucket = i
			break
		}
	}
	c.DurationHistogram[bucket]++
}

func (o *upstreamFeedbackObserver) readTimingSnapshot(now time.Time) *netproto.VideoUpstreamReadTimingDiagnostics {
	if o == nil {
		return nil
	}
	d := &o.readTiming
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.counters
	result.StartedAt = o.started.UnixMilli()
	result.SampleMS = max(int64(0), now.Sub(o.started).Milliseconds())
	result.Closed = d.closed.Load()
	result.SampleEvery = upstreamReadSampleEvery
	result.SampledCalls = d.sampled.Load()
	result.ReadCalls = d.calls.Load()
	return &result
}
