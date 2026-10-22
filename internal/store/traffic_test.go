package store

import (
	"context"
	"noxa/internal/traffic"
	"strings"
	"testing"
	"time"
)

func TestTrafficCheckpointsAreIdempotentAndRetainLifetime(t *testing.T) {
	s := pollTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(5 * time.Minute).Unix()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	save := func(id string, total traffic.Counts, buckets []traffic.Bucket) {
		t.Helper()
		if err := s.SaveTrafficCheckpoint(ctx, id, now, total, buckets); err != nil {
			t.Fatal(err)
		}
	}
	old := now - int64(32*24*time.Hour/time.Second)
	save(a, traffic.Counts{In: 100, Out: 200}, []traffic.Bucket{{Start: old, Counts: traffic.Counts{In: 10, Out: 20}}, {Start: now, Counts: traffic.Counts{In: 90, Out: 180}}})
	// Retried checkpoints and stale lower checkpoints must not count again.
	save(a, traffic.Counts{In: 100, Out: 200}, []traffic.Bucket{{Start: now, Counts: traffic.Counts{In: 90, Out: 180}}})
	save(a, traffic.Counts{In: 1, Out: 2}, []traffic.Bucket{{Start: now, Counts: traffic.Counts{In: 1, Out: 2}}})
	save(b, traffic.Counts{In: 3, Out: 4}, []traffic.Bucket{{Start: now, Counts: traffic.Counts{In: 3, Out: 4}}})
	h, err := s.LoadTrafficHistory(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Total != (traffic.Counts{In: 103, Out: 204}) || h.Since != now || len(h.Buckets) != 1 || h.Buckets[0].Counts != (traffic.Counts{In: 93, Out: 184}) {
		t.Fatalf("bad history: %+v", h)
	}
	// A malformed batch rolls back both the lifetime and the bucket updates.
	if err := s.SaveTrafficCheckpoint(ctx, a, now, traffic.Counts{In: 900, Out: 900}, []traffic.Bucket{{Start: now + 1}}); err == nil {
		t.Fatal("accepted invalid bucket")
	}
	h, err = s.LoadTrafficHistory(ctx, now)
	if err != nil || h.Total.In != 103 {
		t.Fatal(h, err)
	}
}
