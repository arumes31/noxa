package store

import (
	"sync"
	"testing"
)

// Review 2a-fix2: N concurrent finalizes of one complete attempt report the
// delivered copy exactly once (the doorbell rings exactly once).
// Review 2a-fix2 (adopted): concurrent finalizes report the delivered copies
// exactly once, so the doorbell rings once.
func TestOlmConcurrentFinalizeDeliversOnce(t *testing.T) {
	for round := 0; round < 5; round++ {
		s, a, _, m, parts := admissionFixture(t)
		if _, e := s.BeginOlmAttempt(t.Context(), a, m); e != nil {
			t.Fatal(e)
		}
		for i, p := range parts {
			if e := s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); e != nil {
				t.Fatal(e)
			}
		}
		const n = 16
		var wg sync.WaitGroup
		var mu sync.Mutex
		total, accepted, errs := 0, 0, 0
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, d, e := s.FinalizeOlmAttemptDelivered(t.Context(), a, m.AttemptID)
				mu.Lock()
				defer mu.Unlock()
				if e != nil {
					errs++
					return
				}
				if r.State == "accepted" {
					accepted++
				}
				total += len(d)
			}()
		}
		wg.Wait()
		t.Logf("round %d: %d concurrent finalizes: accepted receipts %d, errors %d, delivered entries %d", round, n, accepted, errs, total)
		if total != 1 {
			t.Fatalf("delivered reported %d times", total)
		}
	}
}
