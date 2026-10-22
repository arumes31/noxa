package traffic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const CheckpointInterval = 5 * time.Minute
const bucketSeconds int64 = 300

type Bucket struct {
	Start int64
	Counts
}
type History struct {
	Total   Counts
	Buckets []Bucket
	Since   int64
}
type Store interface {
	SaveTrafficCheckpoint(context.Context, string, int64, Counts, []Bucket) error
	LoadTrafficHistory(context.Context, int64) (History, error)
}
type Summary struct {
	SinceStart        Counts  `json:"since_start"`
	Days7             *Counts `json:"days_7"`
	Days30            *Counts `json:"days_30"`
	AllTime           *Counts `json:"all_time"`
	RecordingSince    int64   `json:"recording_since"`
	PersistedAt       int64   `json:"persisted_at"`
	CheckpointSeconds int64   `json:"checkpoint_seconds"`
	BucketSeconds     int64   `json:"bucket_seconds"`
	StorageOK         bool    `json:"storage_ok"`
}
type Recorder struct {
	Counter      *Counter
	store        Store
	instance     string
	started      time.Time
	now          func() time.Time
	mu           sync.Mutex
	flushGate    chan struct{}
	last         Counts
	acknowledged Counts
	own          map[int64]Counts
	saved        map[int64]Counts
	dirty        map[int64]bool
	history      History
	known        bool
	healthy      bool
	persistedAt  int64
	done         chan struct{}
	stop         chan struct{}
	stopOnce     sync.Once
}

func NewRecorder(ctx context.Context, counter *Counter, store Store) (*Recorder, error) {
	if counter == nil || store == nil {
		return nil, errors.New("traffic counter and storage are required")
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	r := &Recorder{Counter: counter, store: store, instance: hex.EncodeToString(entropy[:]), started: time.Now(), now: time.Now,
		flushGate: make(chan struct{}, 1), own: map[int64]Counts{}, saved: map[int64]Counts{}, dirty: map[int64]bool{}, done: make(chan struct{}), stop: make(chan struct{})}
	history, err := store.LoadTrafficHistory(ctx, r.started.Unix())
	if err == nil {
		r.history = history
		r.known = true
		r.healthy = true
	}
	return r, nil // storage failure is visible and retried without blocking traffic
}

func (r *Recorder) observeLocked(now time.Time) {
	current := r.Counter.Snapshot()
	delta := current.Sub(r.last)
	r.last = current
	bucket := now.Unix() / bucketSeconds * bucketSeconds
	if delta.In > 0 || delta.Out > 0 {
		r.own[bucket] = r.own[bucket].Add(delta)
		r.dirty[bucket] = true
	}
	cutoff := now.Add(-31 * 24 * time.Hour).Unix()
	for start := range r.own {
		if start < cutoff {
			delete(r.own, start)
			delete(r.saved, start)
			delete(r.dirty, start)
		}
	}
}

func (r *Recorder) Snapshot() Summary {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeLocked(now)
	result := Summary{SinceStart: r.last, RecordingSince: r.history.Since, PersistedAt: r.persistedAt, CheckpointSeconds: int64(CheckpointInterval / time.Second), BucketSeconds: bucketSeconds, StorageOK: r.healthy}
	if result.RecordingSince == 0 {
		result.RecordingSince = r.started.Unix()
	}
	if !r.known {
		return result
	}
	total := r.history.Total
	var d7, d30 Counts
	for _, bucket := range r.history.Buckets {
		if bucket.Start >= now.Add(-7*24*time.Hour).Unix() {
			d7 = d7.Add(bucket.Counts)
		}
		if bucket.Start >= now.Add(-30*24*time.Hour).Unix() {
			d30 = d30.Add(bucket.Counts)
		}
	}
	for start, value := range r.own {
		delta := value.Sub(r.saved[start])
		if start >= now.Add(-7*24*time.Hour).Unix() {
			d7 = d7.Add(delta)
		}
		if start >= now.Add(-30*24*time.Hour).Unix() {
			d30 = d30.Add(delta)
		}
	}
	// Lifetime uses a monotonic per-instance checkpoint, including any buckets
	// that have aged out of the rolling retention window.
	total = total.Add(r.last.Sub(r.acknowledged))
	result.Days7 = &d7
	result.Days30 = &d30
	result.AllTime = &total
	return result
}

func (r *Recorder) Flush(ctx context.Context) error {
	select {
	case r.flushGate <- struct{}{}:
		defer func() { <-r.flushGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := r.now()
	r.mu.Lock()
	r.observeLocked(now)
	total := r.last
	if r.known && r.healthy && r.persistedAt > 0 && total == r.acknowledged && len(r.dirty) == 0 {
		r.mu.Unlock()
		return nil
	}
	batch := make([]Bucket, 0, len(r.dirty))
	for start := range r.dirty {
		batch = append(batch, Bucket{start, r.own[start]})
	}
	r.mu.Unlock()
	if err := r.store.SaveTrafficCheckpoint(ctx, r.instance, r.started.Unix(), total, batch); err != nil {
		r.mu.Lock()
		r.healthy = false
		r.mu.Unlock()
		return err
	}
	history, err := r.store.LoadTrafficHistory(ctx, now.Unix())
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.healthy = false
		return err
	}
	r.history = history
	r.known = true
	r.healthy = true
	r.persistedAt = now.Unix()
	r.acknowledged = total
	for _, bucket := range batch {
		r.saved[bucket.Start] = bucket.Counts
		if r.own[bucket.Start] == bucket.Counts {
			delete(r.dirty, bucket.Start)
		}
	}
	return nil
}
func (r *Recorder) Start() {
	go func() {
		defer close(r.done)
		observe := time.NewTicker(time.Minute)
		defer observe.Stop()
		checkpoint := time.NewTicker(CheckpointInterval)
		defer checkpoint.Stop()
		for {
			select {
			case <-observe.C:
				r.mu.Lock()
				r.observeLocked(r.now())
				r.mu.Unlock()
			case <-checkpoint.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = r.Flush(ctx)
				cancel()
			case <-r.stop:
				return
			}
		}
	}()
}
func (r *Recorder) Close(ctx context.Context) error {
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Flush(ctx)
}
