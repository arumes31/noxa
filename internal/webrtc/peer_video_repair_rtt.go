package webrtc

import (
	"math"
	"time"

	pion "github.com/pion/webrtc/v4"
)

const videoRepairRTTFreshness = 10 * time.Second
const videoRepairRTTQueryInterval = time.Second

// Fields are immutable after done closes. Callers must observe done before
// reading rtt/measuredAt; zero rtt means no trustworthy measurement was found.
type videoRepairRTTResult struct {
	done        chan struct{}
	rtt         time.Duration
	measuredAt  time.Time
	requestedAt time.Time
}

// RequestVideoRepairRTT reads existing ICE measurements only on demand. Pion's
// stats getters enter its ICE event loop, so they must not run on the RTP reader.
// At most one query is in flight per peer; no polling or network probes are added.
func (w *PeerConnectionWrapper) RequestVideoRepairRTT() *videoRepairRTTResult {
	return w.requestVideoRepairRTT(time.Now, w.readVideoRepairRTT)
}

func (w *PeerConnectionWrapper) requestVideoRepairRTT(now func() time.Time, query func() (time.Duration, time.Time)) *videoRepairRTTResult {
	w.videoRepairRTTMu.Lock()
	defer w.videoRepairRTTMu.Unlock()
	select {
	case <-w.closed:
		return nil
	default:
	}
	if w.videoRepairRTTInFlight != nil {
		return w.videoRepairRTTInFlight
	}
	at := now()
	if !w.videoRepairRTTCompleted.IsZero() && at.Sub(w.videoRepairRTTCompleted) < videoRepairRTTQueryInterval {
		return nil
	}
	result := &videoRepairRTTResult{done: make(chan struct{}), requestedAt: at}
	w.videoRepairRTTInFlight = result
	go func() {
		var rtt time.Duration
		var measured time.Time
		select {
		case <-w.closed:
		default:
			rtt, measured = query()
		}
		w.videoRepairRTTMu.Lock()
		defer w.videoRepairRTTMu.Unlock()
		select {
		case <-w.closed:
		default:
			result.rtt, result.measuredAt = rtt, measured
		}
		w.videoRepairRTTCompleted = now()
		w.videoRepairRTTInFlight = nil
		close(result.done)
	}()
	return result
}

func (w *PeerConnectionWrapper) readVideoRepairRTT() (time.Duration, time.Time) {
	if w.pc == nil || w.pc.ConnectionState() != pion.PeerConnectionStateConnected || w.pc.SCTP() == nil || w.pc.SCTP().Transport() == nil {
		return 0, time.Time{}
	}
	ice := w.pc.SCTP().Transport().ICETransport()
	if ice == nil {
		return 0, time.Time{}
	}
	before, ok := ice.GetSelectedCandidatePairStats()
	if !ok {
		return 0, time.Time{}
	}
	// The selected-only getter leaves LastResponseTimestamp unimplemented in
	// pinned Pion ICE v4.4.7. Full stats populate the actual STUN response time.
	report := w.pc.GetStats()
	after, ok := ice.GetSelectedCandidatePairStats()
	if !ok || w.pc.ConnectionState() != pion.PeerConnectionStateConnected {
		return 0, time.Time{}
	}
	return videoRepairRTTFromStats(before, after, report, time.Now())
}

func videoRepairRTTFromStats(before, after pion.ICECandidatePairStats, report pion.StatsReport, now time.Time) (time.Duration, time.Time) {
	if before.LocalCandidateID == "" || before.RemoteCandidateID == "" || before.LocalCandidateID != after.LocalCandidateID || before.RemoteCandidateID != after.RemoteCandidateID || before.State != pion.StatsICECandidatePairStateSucceeded || after.State != pion.StatsICECandidatePairStateSucceeded {
		return 0, time.Time{}
	}
	var match *pion.ICECandidatePairStats
	for _, raw := range report {
		stat, ok := raw.(pion.ICECandidatePairStats)
		if !ok || stat.LocalCandidateID != before.LocalCandidateID || stat.RemoteCandidateID != before.RemoteCandidateID {
			continue
		}
		if match != nil {
			return 0, time.Time{}
		}
		match = &stat
	}
	if match == nil || match.State != pion.StatsICECandidatePairStateSucceeded || match.ResponsesReceived == 0 {
		return 0, time.Time{}
	}
	rtt, responseMS := match.CurrentRoundTripTime, float64(match.LastResponseTimestamp)
	nowMS := float64(now.UnixNano()) / float64(time.Millisecond)
	if math.IsNaN(rtt) || math.IsInf(rtt, 0) || rtt <= 0 || math.IsNaN(responseMS) || math.IsInf(responseMS, 0) || responseMS <= 0 || responseMS > nowMS || nowMS-responseMS > float64(videoRepairRTTFreshness/time.Millisecond) {
		return 0, time.Time{}
	}
	// Saturation preserves the maximum repair wait even for a huge, finite RTT,
	// without overflowing time.Duration when the policy later multiplies by two.
	rtt = min(rtt, videoReorderWait.Seconds())
	return max(time.Nanosecond, time.Duration(rtt*float64(time.Second))), match.LastResponseTimestamp.Time()
}
