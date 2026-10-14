package webrtc

import "github.com/pion/interceptor"

// Dispatch works without diagnostics. The optional single-construction handoff
// attributes its counters to the same peer as the actual-write observer.
type upstreamTWCCDispatchFactory struct{ registry *upstreamFeedbackRegistry }

func (f upstreamTWCCDispatchFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	d := newUpstreamTWCCDispatch()
	if r := f.registry; r != nil {
		r.mu.Lock()
		if r.lease != nil && !r.lease.invalid && r.pendingID == id && r.pending != nil {
			r.pending.dispatch = d
		}
		r.mu.Unlock()
	}
	return d, nil
}
