package webrtc

import (
	"context"
	"sync"
)

// contextRWMutex is a writer-preferring gate with cancellable writer waits.
// Its zero value is ready to use and must not be copied. No goroutine is left
// waiting to acquire a lock after cancellation.
type contextRWMutex struct {
	mu      sync.Mutex
	readers int
	writers int
	locked  bool
	changed chan struct{}
}

func (m *contextRWMutex) changedLocked() chan struct{} {
	if m.changed == nil {
		m.changed = make(chan struct{})
	}
	return m.changed
}

func (m *contextRWMutex) notifyLocked() {
	if m.changed != nil {
		close(m.changed)
		m.changed = nil
	}
}

func (m *contextRWMutex) Lock() { _ = m.LockContext(context.Background()) }

func (m *contextRWMutex) LockContext(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writers++
	defer func() { m.writers--; m.notifyLocked() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.locked && m.readers == 0 {
			m.locked = true
			return nil
		}
		changed := m.changedLocked()
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		m.mu.Lock()
	}
}

func (m *contextRWMutex) Unlock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.locked {
		panic("unlock of unlocked context RW mutex")
	}
	m.locked = false
	m.notifyLocked()
}

func (m *contextRWMutex) RLock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.locked || m.writers != 0 {
		changed := m.changedLocked()
		m.mu.Unlock()
		<-changed
		m.mu.Lock()
	}
	m.readers++
}

// TryRLock drops realtime work while an update is pending. It never waits for
// a writer that may itself be joining the caller's media worker.
func (m *contextRWMutex) TryRLock() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked || m.writers != 0 {
		return false
	}
	m.readers++
	return true
}

func (m *contextRWMutex) RUnlock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readers == 0 {
		panic("read unlock of unlocked context RW mutex")
	}
	m.readers--
	if m.readers == 0 {
		m.notifyLocked()
	}
}
