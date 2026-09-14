package server

import (
	"io"
	"testing"
	"testing/synctest"
	"time"
)

// Regression for the old zero-lastActive bypass: even a peer that sends no
// bytes must lose its slot at the short authentication deadline.
func TestPreAuthIdleNeverTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, peer, done := startPreauthPipe(t, 1, false)
		defer finishPreauthPipe(peer, done)
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if got := s.clientCount(); got != 1 {
			t.Fatalf("connection count before deadline = %d, want 1", got)
		}
		time.Sleep(time.Nanosecond)
		assertPreauthExpired(t, s, done)
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("expired peer read = %v, want EOF", err)
		}
	})
}
