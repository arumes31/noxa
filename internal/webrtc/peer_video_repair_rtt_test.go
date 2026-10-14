package webrtc

import (
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func TestVideoRepairRTTUsesActualResponseOnStableSelectedPair(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	selected := pion.ICECandidatePairStats{LocalCandidateID: "local", RemoteCandidateID: "remote", State: pion.StatsICECandidatePairStateSucceeded}
	// Selected-only Pion stats have no response timestamp. Collection Timestamp
	// must never substitute for the full report's actual response timestamp.
	selected.Timestamp = pion.StatsTimestamp(now.UnixMilli())
	full := selected
	full.ResponsesReceived = 12
	full.CurrentRoundTripTime = .025
	full.LastResponseTimestamp = pion.StatsTimestamp(now.Add(-time.Second).UnixMilli())
	cases := []struct {
		name   string
		change func(*pion.ICECandidatePairStats, *pion.ICECandidatePairStats, *pion.ICECandidatePairStats)
		want   time.Duration
	}{
		{name: "actual full report despite absent selected timestamp", want: 25 * time.Millisecond},
		{name: "exact freshness boundary", change: func(_, _, s *pion.ICECandidatePairStats) {
			s.LastResponseTimestamp = pion.StatsTimestamp(now.Add(-videoRepairRTTFreshness).UnixMilli())
		}, want: 25 * time.Millisecond},
		{name: "huge finite RTT saturates", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = math.MaxFloat64 }, want: videoReorderWait},
		{name: "tiny positive RTT stays positive", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = math.SmallestNonzeroFloat64 }, want: time.Nanosecond},
		{name: "changed selected pair", change: func(_, a, _ *pion.ICECandidatePairStats) { a.RemoteCandidateID = "replacement" }},
		{name: "missing pair identity", change: func(b, _, _ *pion.ICECandidatePairStats) { b.LocalCandidateID = "" }},
		{name: "other pair only", change: func(_, _, s *pion.ICECandidatePairStats) { s.LocalCandidateID = "other" }},
		{name: "selected no longer succeeded", change: func(_, a, _ *pion.ICECandidatePairStats) { a.State = pion.StatsICECandidatePairStateFailed }},
		{name: "report not succeeded", change: func(_, _, s *pion.ICECandidatePairStats) { s.State = pion.StatsICECandidatePairStateInProgress }},
		{name: "no responses", change: func(_, _, s *pion.ICECandidatePairStats) { s.ResponsesReceived = 0 }},
		{name: "missing actual timestamp with fresh collection timestamp", change: func(_, _, s *pion.ICECandidatePairStats) { s.LastResponseTimestamp = 0 }},
		{name: "stale actual response", change: func(_, _, s *pion.ICECandidatePairStats) {
			s.LastResponseTimestamp = pion.StatsTimestamp(now.Add(-videoRepairRTTFreshness - time.Millisecond).UnixMilli())
		}},
		{name: "future response", change: func(_, _, s *pion.ICECandidatePairStats) {
			s.LastResponseTimestamp = pion.StatsTimestamp(now.Add(time.Millisecond).UnixMilli())
		}},
		{name: "NaN response", change: func(_, _, s *pion.ICECandidatePairStats) { s.LastResponseTimestamp = pion.StatsTimestamp(math.NaN()) }},
		{name: "infinite response", change: func(_, _, s *pion.ICECandidatePairStats) { s.LastResponseTimestamp = pion.StatsTimestamp(math.Inf(1)) }},
		{name: "zero RTT", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = 0 }},
		{name: "negative RTT", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = -.1 }},
		{name: "NaN RTT", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = math.NaN() }},
		{name: "infinite RTT", change: func(_, _, s *pion.ICECandidatePairStats) { s.CurrentRoundTripTime = math.Inf(1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after, stat := selected, selected, full
			if tc.change != nil {
				tc.change(&before, &after, &stat)
			}
			got, at := videoRepairRTTFromStats(before, after, pion.StatsReport{"pair": stat}, now)
			if got != tc.want {
				t.Fatalf("RTT=%v, want %v", got, tc.want)
			}
			if got == 0 && !at.IsZero() {
				t.Fatal("invalid RTT retained timestamp")
			}
			if got > 0 && !at.Equal(stat.LastResponseTimestamp.Time()) {
				t.Fatal("timestamp was not actual last response")
			}
		})
	}
	for name, report := range map[string]pion.StatsReport{"empty": {}, "duplicate identity": {"one": full, "two": full}} {
		t.Run(name, func(t *testing.T) {
			if got, at := videoRepairRTTFromStats(selected, selected, report, now); got != 0 || !at.IsZero() {
				t.Fatal("ambiguous or missing report accepted")
			}
		})
	}
}

func TestVideoRepairRTTQueriesCoalesceAndRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &PeerConnectionWrapper{closed: make(chan struct{})}
		started, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		query := func() (time.Duration, time.Time) {
			calls.Add(1)
			close(started)
			<-release
			return 30 * time.Millisecond, time.Now().Add(-time.Second)
		}
		first := w.requestVideoRepairRTT(time.Now, query)
		<-started
		for range 100 {
			if got := w.requestVideoRepairRTT(time.Now, query); got != first {
				t.Fatal("in-flight query was not shared")
			}
		}
		select {
		case <-first.done:
			t.Fatal("pending query completed")
		default:
		}
		if calls.Load() != 1 {
			t.Fatal("more than one worker")
		}
		close(release)
		<-first.done
		if first.rtt != 30*time.Millisecond || first.measuredAt.IsZero() || first.requestedAt.IsZero() {
			t.Fatal("result missing immutable measured values")
		}
		nextQuery := func() (time.Duration, time.Time) { calls.Add(1); return 0, time.Time{} }
		if got := w.requestVideoRepairRTT(time.Now, nextQuery); got != nil {
			t.Fatal("completed result reused or immediate query allowed")
		}
		time.Sleep(videoRepairRTTQueryInterval - time.Nanosecond)
		if got := w.requestVideoRepairRTT(time.Now, nextQuery); got != nil {
			t.Fatal("cooldown ended early")
		}
		time.Sleep(time.Nanosecond)
		next := w.requestVideoRepairRTT(time.Now, nextQuery)
		if next == nil || next == first {
			t.Fatal("new request did not get a fresh future")
		}
		<-next.done
		if next.rtt != 0 || !next.measuredAt.IsZero() || calls.Load() != 2 {
			t.Fatal("invalid measurement was not completed as unknown")
		}
	})
}

func TestVideoRepairRTTCloseRejectsAndInvalidatesPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &PeerConnectionWrapper{closed: make(chan struct{})}
		started, release := make(chan struct{}), make(chan struct{})
		result := w.requestVideoRepairRTT(time.Now, func() (time.Duration, time.Time) { close(started); <-release; return time.Second, time.Now() })
		<-started
		close(w.closed)
		if next := w.requestVideoRepairRTT(time.Now, func() (time.Duration, time.Time) { t.Error("closed query executed"); return 0, time.Time{} }); next != nil {
			t.Fatal("closed peer admitted query")
		}
		close(release)
		<-result.done
		if result.rtt != 0 || !result.measuredAt.IsZero() {
			t.Fatal("closed peer published pending RTT")
		}
	})
}

func TestVideoRepairRTTUnavailablePeerCompletesUnknown(t *testing.T) {
	w := &PeerConnectionWrapper{closed: make(chan struct{})}
	result := w.RequestVideoRepairRTT()
	select {
	case <-result.done:
		if result.rtt != 0 || !result.measuredAt.IsZero() {
			t.Fatal("missing PC produced RTT")
		}
	case <-time.After(time.Second):
		t.Fatal("unknown result never completed")
	}
}
