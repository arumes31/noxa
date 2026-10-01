package webrtc

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func waitPendingVideoWriter(t *testing.T, m *contextRWMutex) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		pending := m.writers != 0
		m.mu.Unlock()
		if pending {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("writer did not queue")
}

func TestContextRWMutexCancellationReleasesReaderQueue(t *testing.T) {
	var m contextRWMutex
	m.RLock()
	defer m.RUnlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := make(chan error, 1)
	go func() { writer <- m.LockContext(ctx) }()
	waitPendingVideoWriter(t, &m)
	reader := make(chan struct{})
	go func() { m.RLock(); close(reader); m.RUnlock() }()
	select {
	case <-reader:
		t.Fatal("new reader bypassed queued writer")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-writer; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-reader:
	case <-time.After(time.Second):
		t.Fatal("canceled writer stranded readers")
	}
}

func TestContextRWMutexConcurrentReadersAndWriters(t *testing.T) {
	var m contextRWMutex
	var wg sync.WaitGroup
	value := 0
	for range 8 {
		wg.Go(func() {
			for range 100 {
				m.Lock()
				value++
				m.Unlock()
				m.RLock()
				if value < 1 {
					t.Error("lost protected value")
				}
				m.RUnlock()
			}
		})
	}
	wg.Wait()
	if value != 800 {
		t.Fatal(value)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("already-canceled waiter acquired free gate")
	}
	m.Lock()
	value++
	m.Unlock()
	if value != 801 {
		t.Fatal("cancellation stranded the next writer")
	}
}
