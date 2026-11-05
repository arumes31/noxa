package query

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// blockedWriter models a peer that stopped reading: Write blocks until the
// transport is closed.
type blockedWriter struct {
	once   sync.Once
	closed chan struct{}
}

func (w *blockedWriter) Write([]byte) (int, error) {
	<-w.closed
	return 0, errClosedForTest
}

func (w *blockedWriter) close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

type testError string

func (e testError) Error() string { return string(e) }

const errClosedForTest = testError("closed")

// A reply to a client that never reads is abandoned after the write timeout
// instead of holding the session (and its connection slot) forever.
func TestReplyToNonReadingClientTimesOut(t *testing.T) {
	old := responseWriteTimeout
	responseWriteTimeout = 50 * time.Millisecond
	t.Cleanup(func() { responseWriteTimeout = old })
	w := &blockedWriter{closed: make(chan struct{})}
	sess := &session{
		w:                w,
		setWriteDeadline: func(time.Time) error { return nil },
		closeTransport:   w.close,
	}
	s := New("127.0.0.1:0", zap.NewNop(), nil)
	done := make(chan bool, 1)
	go func() { done <- s.write(sess, "help text\n") }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("blocked write reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write to a non-reading client did not time out")
	}
}
