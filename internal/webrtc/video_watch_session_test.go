package webrtc

import (
	"testing"
	"time"
)

func TestVideoWatchExistingSessionDoesNotWaitForPacketLease(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "viewer")
	session := r.VideoWatchSession("viewer")
	r.watchMu.RLock()
	done := make(chan uint64, 1)
	go func() { done <- r.VideoWatchSession("viewer") }()
	select {
	case got := <-done:
		r.watchMu.RUnlock()
		if got != session || got == 0 {
			t.Fatalf("session changed: %d -> %d", session, got)
		}
	case <-time.After(time.Second):
		r.watchMu.RUnlock()
		<-done
		t.Fatal("existing session lookup waits for exclusive watch lease while a packet is writing")
	}
}

func TestVideoWatchSessionConcurrentCreationAndInvalidation(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "viewer")
	for cycle := range 3 {
		done := make(chan uint64, 32)
		for range 32 {
			go func() { done <- r.VideoWatchSession("viewer") }()
		}
		session := <-done
		if session == 0 {
			t.Fatal("missing watch session")
		}
		for range 31 {
			if got := <-done; got != session {
				t.Fatalf("concurrent lookup created different session: %d != %d", got, session)
			}
		}
		r.DetachPeerKeepChannel("viewer")
		if got := r.VideoWatchSession("viewer"); got == 0 || got == session {
			t.Fatalf("cycle %d reused invalidated session %d", cycle, got)
		}
	}
	r.LeaveChannel(1, "viewer")
	if got := r.VideoWatchSession("viewer"); got != 0 {
		t.Fatalf("departed viewer retained session %d", got)
	}
}
