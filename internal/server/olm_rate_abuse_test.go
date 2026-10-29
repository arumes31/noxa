package server

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// Review 2a-fix2: the per-(sender, target) control limiter is charged BEFORE
// the account cap, keyed by client-supplied (unvalidated) target strings, and
// chatRateLimiter never prunes buckets.
// Review 2a-fix2 P2-1 (repro adopted): 200k refused control begins with
// fresh client-chosen targets leave bounded limiter state.
func TestOlmPerTargetLimiterStaysBounded(t *testing.T) {
	srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{})
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	const n = 200000
	admitted := 0
	pad := strings.Repeat("x", 100)
	for i := 0; i < n; i++ {
		m := &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true},
			{Principal: "p" + pad + fmt.Sprint(i), Device: "d" + pad + fmt.Sprint(i)}}}
		if srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: "mallory", Device: "m1"}, netproto.OlmAdmissionRequest{Action: "begin", Manifest: m}) {
			admitted++
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	srv.olmBudget.perTarget.mu.Lock()
	buckets := len(srv.olmBudget.perTarget.buckets)
	srv.olmBudget.perTarget.mu.Unlock()
	t.Logf("begins=%d admitted=%d perTarget buckets=%d heap growth=%.1f MiB (%.0f B/entry)", n, admitted, buckets, float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(max(buckets, 1)))
	if buckets > 10*olmAccountControlBegins {
		t.Errorf("%d refused begins created %d limiter entries", n-admitted, buckets)
	}
}

// Review 2a-fix2 P2-2 (repro adopted): drains of real deliveries are never
// refused for a multi-device recipient: one sender at the chat limit against
// 10 devices, and two senders against 5 devices (32 messages per window).
func TestOlmDrainsOfMultiDeviceRecipientsAreNeverRefused(t *testing.T) {
	for _, c := range []struct{ devices, messages int }{{3, 16}, {5, 16}, {8, 16}, {10, 16}, {5, 32}, {10, 32}} {
		devices := c.devices
		srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{})
		// One 10 s window: 16 incoming fanout messages (1.67/s, one sender at
		// the chat limit) plus each device's 5 s fallback poll (2 per window).
		refused, firstRefusal := 0, -1
		for d := 0; d < devices; d++ {
			for p := 0; p < 2; p++ {
				srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: "victim", Device: fmt.Sprint("v", d)}, netproto.OlmAdmissionRequest{Action: "deliveries"})
			}
		}
		for msg := 0; msg < c.messages; msg++ {
			for d := 0; d < devices; d++ {
				for _, action := range []string{"deliveries", "receive_ack"} {
					if !srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: "victim", Device: fmt.Sprint("v", d)}, netproto.OlmAdmissionRequest{Action: action}) {
						refused++
						if firstRefusal < 0 {
							firstRefusal = msg
						}
					}
				}
			}
		}
		t.Logf("recipient with %2d devices, %d messages: %d of %d drain frames refused (first at message %d)", devices, c.messages, refused, c.messages*2*devices, firstRefusal)
		if refused != 0 {
			t.Fatalf("%d drain frames refused", refused)
		}
	}
}
