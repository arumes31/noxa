package server

import (
	"context"
	"sync"
)

// contextMutex serializes effects while allowing a bounded operation
// to abandon the writer queue. Its zero value is ready to use; do not copy it.
type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) semaphore() chan struct{} {
	m.once.Do(func() { m.token = make(chan struct{}, 1); m.token <- struct{}{} })
	return m.token
}

func (m *contextMutex) Lock() { <-m.semaphore() }

func (m *contextMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.semaphore():
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

func (m *contextMutex) TryLock() bool {
	select {
	case <-m.semaphore():
		return true
	default:
		return false
	}
}

func (m *contextMutex) Unlock() {
	select {
	case m.semaphore() <- struct{}{}:
	default:
		panic("unlock of unlocked context mutex")
	}
}
