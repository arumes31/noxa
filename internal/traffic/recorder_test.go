package traffic

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu                 sync.Mutex
	totals             map[string]Counts
	buckets            map[string]map[int64]Counts
	since              int64
	saves, loads       int
	failSave, failLoad bool
	afterSave          func()
}

func newMemoryStore() *memoryStore {
	return &memoryStore{totals: map[string]Counts{}, buckets: map[string]map[int64]Counts{}}
}
func (s *memoryStore) SaveTrafficCheckpoint(_ context.Context, id string, start int64, total Counts, batch []Bucket) error {
	s.mu.Lock()
	s.saves++
	s.totals[id] = total
	if s.since == 0 || start < s.since {
		s.since = start
	}
	if s.buckets[id] == nil {
		s.buckets[id] = map[int64]Counts{}
	}
	for _, b := range batch {
		s.buckets[id][b.Start] = b.Counts
	}
	fail, after := s.failSave, s.afterSave
	s.mu.Unlock()
	if after != nil {
		after()
	}
	if fail {
		return errors.New("ambiguous commit")
	}
	return nil
}
func (s *memoryStore) LoadTrafficHistory(_ context.Context, _ int64) (History, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.failLoad {
		return History{}, errors.New("unavailable")
	}
	h := History{Since: s.since}
	for id, total := range s.totals {
		h.Total = h.Total.Add(total)
		for start, c := range s.buckets[id] {
			h.Buckets = append(h.Buckets, Bucket{start, c})
		}
	}
	return h, nil
}
func fixtureRecorder(t *testing.T, s *memoryStore) (*Recorder, *time.Time) {
	t.Helper()
	r, err := NewRecorder(t.Context(), &Counter{}, s)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(5 * time.Minute)
	r.started = now
	r.now = func() time.Time { return now }
	return r, &now
}
func TestRecorderReadsRAMAndRetriesWithoutDoubleCounting(t *testing.T) {
	s := newMemoryStore()
	r, now := fixtureRecorder(t, s)
	r.Counter.In(100)
	r.Counter.Out(200)
	for range 50 {
		v := r.Snapshot()
		if v.AllTime == nil || *v.AllTime != (Counts{100, 200}) {
			t.Fatal(v)
		}
	}
	if s.loads != 1 || s.saves != 0 {
		t.Fatalf("UI touched storage: loads=%d saves=%d", s.loads, s.saves)
	}
	if CheckpointInterval != 5*time.Minute {
		t.Fatal(CheckpointInterval)
	}
	s.failSave = true
	if r.Flush(t.Context()) == nil {
		t.Fatal("lost storage failure")
	}
	v := r.Snapshot()
	if v.StorageOK || *v.AllTime != (Counts{100, 200}) {
		t.Fatal(v)
	}
	s.failSave = false
	s.afterSave = func() { r.Counter.In(5); r.Snapshot() }
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.afterSave = nil
	v = r.Snapshot()
	if !v.StorageOK || *v.AllTime != (Counts{105, 200}) || *v.Days7 != (Counts{105, 200}) || v.PersistedAt != now.Unix() {
		t.Fatal(v)
	}
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	r2, _ := fixtureRecorder(t, s)
	r2.Counter.In(7)
	if got := r2.Snapshot(); *got.AllTime != (Counts{112, 200}) || got.SinceStart != (Counts{7, 0}) {
		t.Fatal(got)
	}
}
func TestRecorderRollingWindowsAndLifetimeSurvivePruning(t *testing.T) {
	s := newMemoryStore()
	r, now := fixtureRecorder(t, s)
	r.Counter.In(10)
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	r.Counter.In(20)
	v := r.Snapshot()
	if v.Days7.In != 20 || v.Days30.In != 30 || v.AllTime.In != 30 {
		t.Fatal(v)
	}
	*now = now.Add(24 * 24 * time.Hour)
	r.Counter.In(40)
	v = r.Snapshot()
	if v.Days7.In != 40 || v.Days30.In != 60 || v.AllTime.In != 70 {
		t.Fatal(v)
	}
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	v = r.Snapshot()
	if v.AllTime.In != 70 || v.Days30.In != 60 {
		t.Fatal(v)
	}
}
func TestRecorderStorageRecoveryAndShutdown(t *testing.T) {
	s := newMemoryStore()
	s.failLoad = true
	r, _ := fixtureRecorder(t, s)
	r.Counter.In(9)
	v := r.Snapshot()
	if v.AllTime != nil || v.StorageOK || v.SinceStart.In != 9 {
		t.Fatal(v)
	}
	s.failLoad = false
	r.Start()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.saves != 1 {
		t.Fatal(s.saves)
	}
	if r.Snapshot().AllTime.In != 9 {
		t.Fatal("shutdown lost bytes")
	}
}
func TestRecorderFlushGateRespectsCancellation(t *testing.T) {
	r, _ := fixtureRecorder(t, newMemoryStore())
	r.flushGate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-r.flushGate
}

func TestIdleRecorderSkipsUnchangedWrites(t *testing.T) {
	s := newMemoryStore()
	r, _ := fixtureRecorder(t, s)
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := r.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if s.saves != 1 {
		t.Fatalf("idle writes: %d", s.saves)
	}
	r.Counter.In(1)
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.saves != 2 {
		t.Fatal("new traffic not persisted")
	}
}
