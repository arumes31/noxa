package webrtc

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestAdaptiveVideoRepairWait(t *testing.T) {
	for _, tc := range []struct{ rtt, want time.Duration }{
		{0, videoReorderWait}, {-time.Second, videoReorderWait},
		{time.Millisecond, 250 * time.Millisecond},
		{40 * time.Millisecond, 280 * time.Millisecond},
		{400 * time.Millisecond, time.Second},
		{time.Second, 2200 * time.Millisecond},
		{2 * time.Second, videoReorderWait},
		{time.Duration(1<<63 - 1), videoReorderWait},
	} {
		if got := adaptiveVideoRepairWait(tc.rtt); got != tc.want {
			t.Errorf("RTT %v: got %v, want %v", tc.rtt, got, tc.want)
		}
	}
}

func TestVideoReorderAdaptiveDeadlineKeepsOriginalSuffixAge(t *testing.T) {
	var q videoReorder
	now := time.Unix(1, 0)
	var got []uint16
	emit := func(p *rtp.Packet, _ string) { got = append(got, p.SequenceNumber) }
	q.push(boundsDeltaPacket(1, 1), "video/VP8", now, emit)
	q.push(boundsDeltaPacket(3, 3), "video/VP8", now, emit)
	q.push(boundsDeltaPacket(5, 5), "video/VP8", now.Add(50*time.Millisecond), emit)
	epoch := q.epoch
	q.applyRepairWait(epoch, 280*time.Millisecond, now, now)
	q.applyRepairWait(epoch, time.Second, now, now) // A later estimate must not extend the wait.
	if !q.deadline().Equal(now.Add(280 * time.Millisecond)) {
		t.Fatalf("original gap moved: %v", q.deadline())
	}
	q.push(boundsDeltaPacket(2, 2), "video/VP8", now.Add(100*time.Millisecond), emit)
	if !q.deadline().Equal(now.Add(330 * time.Millisecond)) {
		t.Fatalf("remaining suffix lost its original age: %v", q.deadline())
	}
	q.flush(now.Add(280*time.Millisecond), false, emit)
	if len(got) != 3 {
		t.Fatal("later gap expired at the repaired predecessor's deadline")
	}
	q.flush(now.Add(330*time.Millisecond), false, emit)
	if len(got) != 4 || got[3] != 5 || !q.deadline().IsZero() {
		t.Fatalf("adaptive suffix did not expire: %v", got)
	}
	q.push(boundsDeltaPacket(7, 7), "video/VP8", now.Add(time.Second), emit)
	q.applyRepairWait(epoch, videoReorderMinWait, now, now)
	if q.epoch == epoch || !q.deadline().Equal(now.Add(time.Second+videoReorderWait)) {
		t.Fatal("old episode shortened a new gap")
	}
}

func TestVideoReorderAdaptiveReadUsesOriginalGapOpening(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		queryDelay, rtt, measuredOffset, want time.Duration
	}{
		{"low RTT", 100 * time.Millisecond, 40 * time.Millisecond, 0, 280 * time.Millisecond},
		{"high RTT", 100 * time.Millisecond, 400 * time.Millisecond, 0, time.Second},
		{"ceiling", 100 * time.Millisecond, 2 * time.Second, 0, videoReorderWait},
		{"unknown", 100 * time.Millisecond, 0, 0, videoReorderWait},
		{"stale", 100 * time.Millisecond, 40 * time.Millisecond, -11 * time.Second, videoReorderWait},
		{"future", 100 * time.Millisecond, 40 * time.Millisecond, time.Second, videoReorderWait},
		{"slow query", 500 * time.Millisecond, 40 * time.Millisecond, 0, 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				track := &repairPollingTrack{initial: []*rtp.Packet{repairPollingFragment(1), repairPollingFragment(3)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
				started := time.Now()
				queries, delivered, complete := 0, 0, 0
				var frame h264ForwardFrame
				request := func() *videoRepairRTTResult {
					queries++
					r := &videoRepairRTTResult{done: make(chan struct{}), requestedAt: time.Now()}
					go func() {
						time.Sleep(tc.queryDelay)
						r.rtt, r.measuredAt = tc.rtt, time.Now().Add(tc.measuredOffset)
						close(r.done)
					}()
					return r
				}
				readOrderedVideoAdaptive(track, func() bool { return delivered < 2 }, func() string { return pion.MimeTypeH264 }, func(p *rtp.Packet, _ string) {
					delivered++
					if packets, _, _ := frame.push(p, time.Now()); len(packets) > 0 {
						complete++
					}
				}, nil, nil, request)
				latest := tc.want
				if tc.queryDelay >= tc.want {
					// A result racing the reader's poll is consumed by the next
					// existing repair poll; it does not wake a primary read.
					latest += videoRepairPoll
				}
				if elapsed := time.Since(started); elapsed < tc.want || elapsed > latest || queries != 1 || complete != 0 {
					t.Fatalf("elapsed=%v want=[%v,%v] queries=%d incomplete frames forwarded=%d", elapsed, tc.want, latest, queries, complete)
				}
			})
		})
	}
}

func TestVideoReorderAdaptiveDoesNotQueryContiguousTraffic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{repairPollingFragment(1), repairPollingFragment(2)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		go func() { time.Sleep(time.Second); close(track.closed) }()
		readOrderedVideoAdaptive(track, func() bool { return true }, func() string { return pion.MimeTypeH264 }, func(*rtp.Packet, string) {}, nil, nil, func() *videoRepairRTTResult {
			t.Fatal("contiguous traffic triggered RTT work")
			return nil
		})
		if track.timeouts != 0 || track.gapDeadlines != 0 {
			t.Fatal("contiguous traffic acquired polling")
		}
	})
}

func TestVideoReorderAdaptiveRepairBoundary(t *testing.T) {
	for _, delay := range []time.Duration{279 * time.Millisecond, 281 * time.Millisecond} {
		var q videoReorder
		now := time.Unix(1, 0)
		var got []uint16
		emit := func(p *rtp.Packet, _ string) { got = append(got, p.SequenceNumber) }
		q.push(boundsDeltaPacket(1, 1), "video/VP8", now, emit)
		q.push(boundsDeltaPacket(3, 3), "video/VP8", now, emit)
		q.applyRepairWait(q.epoch, 280*time.Millisecond, now, now)
		q.flush(now.Add(delay), false, emit)
		q.push(boundsDeltaPacket(2, 2), "video/VP8", now.Add(delay), emit)
		if delay < 280*time.Millisecond {
			if len(got) != 3 || got[1] != 2 || got[2] != 3 {
				t.Fatalf("timely repair lost: %v", got)
			}
		} else if len(got) != 2 || got[1] != 3 {
			t.Fatalf("late repair replayed abandoned data: %v", got)
		}
	}
}

func TestVideoReorderAdaptiveHigherRTTOnlyChangesFuturePackets(t *testing.T) {
	var q videoReorder
	now := time.Unix(1, 0)
	emit := func(*rtp.Packet, string) {}
	q.push(boundsDeltaPacket(1, 1), pion.MimeTypeVP8, now, emit)
	q.push(boundsDeltaPacket(3, 3), pion.MimeTypeVP8, now, emit)
	q.applyRepairWait(q.epoch, 280*time.Millisecond, now, now)
	q.push(boundsDeltaPacket(5, 5), pion.MimeTypeVP8, now.Add(50*time.Millisecond), emit)
	q.applyRepairWait(q.epoch, time.Second, now.Add(100*time.Millisecond), now.Add(100*time.Millisecond))
	q.push(boundsDeltaPacket(7, 7), pion.MimeTypeVP8, now.Add(150*time.Millisecond), emit)
	for seq, want := range map[uint16]time.Duration{3: 280 * time.Millisecond, 5: 330 * time.Millisecond, 7: 1150 * time.Millisecond} {
		if !q.pending[seq].expires.Equal(now.Add(want)) {
			t.Fatalf("packet %d deadline=%v want=%v", seq, q.pending[seq].expires, now.Add(want))
		}
	}
	q.flush(now.Add(330*time.Millisecond), false, emit)
	if len(q.pending) != 1 || !q.deadline().Equal(now.Add(1150*time.Millisecond)) {
		t.Fatal("higher-RTT suffix inherited old short deadline")
	}
}

func TestVideoReorderAdaptivePersistentQueueExpiresEstimate(t *testing.T) {
	var q videoReorder
	now := time.Unix(1, 0)
	emit := func(*rtp.Packet, string) {}
	q.push(boundsDeltaPacket(1, 1), pion.MimeTypeVP8, now, emit)
	q.push(boundsDeltaPacket(3, 3), pion.MimeTypeVP8, now, emit)
	q.applyRepairWait(q.epoch, videoReorderMinWait, now, now)
	epoch := q.epoch
	for i := 1; i <= 55; i++ {
		at := now.Add(time.Duration(i) * 200 * time.Millisecond)
		seq := uint16(3 + 2*i)
		q.push(boundsDeltaPacket(seq, uint32(seq)), pion.MimeTypeVP8, at, emit)
		want := videoReorderMinWait
		if i > 50 {
			want = videoReorderWait
		}
		if !q.pending[seq].expires.Equal(at.Add(want)) {
			t.Fatalf("arrival %d retained stale budget: %v", i, q.pending[seq].expires.Sub(at))
		}
		q.flush(at, false, emit)
		if len(q.pending) == 0 || q.epoch != epoch {
			t.Fatal("fixture did not preserve nonempty episode")
		}
	}
	// Existing deadlines remain fixed even after another valid higher estimate.
	old := q.pending[105].expires
	q.applyRepairWait(epoch, time.Second, now.Add(11*time.Second), now.Add(11*time.Second))
	if q.pending[105].expires.After(old) {
		t.Fatal("refresh extended an existing deadline")
	}
}

func TestVideoReorderAdaptiveSecondGapRetriesCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{boundsDeltaPacket(1, 1), boundsDeltaPacket(3, 3)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		start := time.Now()
		var queries []time.Duration
		delivered := 0
		request := func() *videoRepairRTTResult {
			queries = append(queries, time.Since(start))
			if len(queries) == 2 {
				return nil
			} // Another peer-level query completed less than 1s ago.
			r := &videoRepairRTTResult{done: make(chan struct{})}
			if len(queries) == 1 {
				go func() {
					time.Sleep(100 * time.Millisecond)
					r.rtt = 40 * time.Millisecond
					r.measuredAt = time.Now()
					close(r.done)
				}()
			} else {
				r.rtt = 40 * time.Millisecond
				r.measuredAt = time.Now()
				close(r.done)
			}
			return r
		}
		readOrderedVideoAdaptive(track, func() bool { return delivered < 3 }, func() string { return pion.MimeTypeVP8 }, func(p *rtp.Packet, _ string) {
			delivered++
			if p.SequenceNumber == 3 {
				track.initial = append(track.initial, boundsDeltaPacket(5, 5))
			}
		}, nil, nil, request)
		if len(queries) != 3 || queries[0] != 0 || queries[1] != time.Second || queries[2] != 2*time.Second || time.Since(start) != 2*time.Second {
			t.Fatalf("cooldown retry: queries=%v elapsed=%v", queries, time.Since(start))
		}
	})
}

func TestVideoReorderAdaptiveNewHeadIgnoresOldFuture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{boundsDeltaPacket(1, 1), boundsDeltaPacket(3, 3), boundsDeltaPacket(5, 5)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		start := time.Now()
		queries, delivered := 0, 0
		go func() { time.Sleep(100 * time.Millisecond); track.repairs <- boundsDeltaPacket(2, 2) }()
		request := func() *videoRepairRTTResult {
			queries++
			r := &videoRepairRTTResult{done: make(chan struct{})}
			if queries == 1 {
				go func() {
					time.Sleep(500 * time.Millisecond)
					r.rtt = 40 * time.Millisecond
					r.measuredAt = time.Now()
					close(r.done)
				}()
			} else {
				r.rtt = 40 * time.Millisecond
				r.measuredAt = time.Now()
				close(r.done)
			}
			return r
		}
		readOrderedVideoAdaptive(track, func() bool { return delivered < 4 }, func() string { return pion.MimeTypeVP8 }, func(p *rtp.Packet, _ string) {
			delivered++
			if p.SequenceNumber == 5 && time.Since(start) < time.Second {
				t.Fatal("previous head future shortened replacement head")
			}
		}, nil, nil, request)
		if queries != 2 || time.Since(start) != time.Second {
			t.Fatalf("new head was not requeried: queries=%d elapsed=%v", queries, time.Since(start))
		}
	})
}

func TestVideoReorderAdaptiveExpiredFUDoesNotReopenTimestamp(t *testing.T) {
	var q videoReorder
	var frame h264ForwardFrame
	now := time.Unix(1, 0)
	var completed [][]*rtp.Packet
	keys, lost := 0, 0
	emit := func(p *rtp.Packet, _ string) {
		packets, key, missing := frame.push(p, now)
		if len(packets) > 0 {
			completed = append(completed, packets)
		}
		if key {
			keys++
		}
		if missing {
			lost++
		}
	}
	q.push(h264ForwardPacket(1, 90, false, 0x7c, 0x85, 1), pion.MimeTypeH264, now, emit)
	q.push(h264ForwardPacket(3, 90, false, 0x7c, 0x05, 3), pion.MimeTypeH264, now, emit)
	q.applyRepairWait(q.epoch, 280*time.Millisecond, now, now)
	now = now.Add(280 * time.Millisecond)
	q.flush(now, false, emit)
	q.push(h264ForwardPacket(4, 90, true, 0x7c, 0x45, 4), pion.MimeTypeH264, now, emit)
	q.push(h264ForwardPacket(2, 90, false, 0x7c, 0x05, 2), pion.MimeTypeH264, now, emit)
	if len(completed) != 0 || lost == 0 {
		t.Fatal("damaged FU reopened or missing loss signal")
	}
	q.push(h264ForwardPacket(5, 180, false, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1), pion.MimeTypeH264, now, emit)
	q.push(h264ForwardPacket(6, 180, true, 0x65, 1), pion.MimeTypeH264, now, emit)
	if len(completed) != 1 || keys != 1 || len(completed[0]) != 2 || completed[0][0].SequenceNumber != 5 {
		t.Fatal("next complete SPS/PPS-IDR did not recover")
	}
}
