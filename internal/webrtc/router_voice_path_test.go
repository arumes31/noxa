package webrtc

import (
	"testing"
	"time"
)

// Speech onset after a long silence must not wait for a contended Router.mu:
// the cached route is served however old it is.
func TestVoiceFanoutServesAgedCacheWhileRouterBusy(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "a")
	r.JoinChannel(1, "a")
	tap := &fakeTrackWriter{}
	r.AddOutput("recorder:1", tap)
	r.JoinChannel(1, "recorder:1")
	if sent := r.ForwardRTP("a", SlotMic, makeAudioPacket(t, 1, -1)); sent != 1 {
		t.Fatalf("uncontended sent = %d, want 1", sent)
	}

	r.mu.Lock()
	time.Sleep(300 * time.Millisecond) // older than any age bound
	done := make(chan int, 1)
	go func() { done <- r.ForwardRTP("a", SlotMic, makeAudioPacket(t, 2, -1)) }()
	select {
	case sent := <-done:
		r.mu.Unlock()
		if sent != 1 || tap.count() != 2 {
			t.Fatalf("contended sent = %d, tap writes = %d", sent, tap.count())
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		<-done
		t.Fatal("voice packet waited for Router.mu despite a cached route")
	}
}

// A sender's cached route is dropped when it leaves or detaches and is never
// resurrected by a packet that computed it before the change.
func TestVoiceFanoutForgottenOnLeaveAndDetach(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "a")
	attachFakePeer(t, e, r, "b")
	r.JoinChannel(1, "a")
	r.JoinChannel(1, "b")
	key := "a" + slotSep + SlotMic
	cached := func() bool { _, ok := r.voicePath.fanouts.Load(key); return ok }

	r.ForwardRTP("a", SlotMic, makeAudioPacket(t, 1, -1))
	if !cached() {
		t.Fatal("route was not cached")
	}
	r.LeaveChannel(1, "a")
	if cached() {
		t.Fatal("leave kept the departed sender's route")
	}

	r.JoinChannel(1, "a")
	r.ForwardRTP("a", SlotMic, makeAudioPacket(t, 2, -1))
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for seq := uint16(3); ; seq++ {
			select {
			case <-stop:
				return
			default:
				r.ForwardRTP("a", SlotMic, makeAudioPacket(t, seq, -1))
			}
		}
	}()
	time.Sleep(10 * time.Millisecond)
	r.DetachPeer("a")
	close(stop)
	<-finished
	// Packets that raced the detach recompute against post-detach state
	// (sender not in a channel) or were stored before the detach deleted
	// them; either way nothing retains the departed sender's outputs.
	if f, ok := r.voicePath.fanouts.Load(key); ok && len(f.(*voiceFanout).writers) != 0 {
		t.Fatal("detach left a cached route with outputs")
	}
}

// Pruning a revoked pair retires its Pion sender after Router.mu is released:
// RemoveTrack can wait behind the recipient's SetRemoteDescription, and
// voice/ingress readers must not queue behind it.
func TestPrunedPairsRetireSendersOutsideRouterLock(t *testing.T) {
	deny := PublisherGuard(func(p PublisherAccess) bool { return p.SubscriberID != "b" || p.PublisherID != "a" })
	for name, prune := range map[string]func(r *Router){
		"set_publisher_guard": func(r *Router) { r.SetPublisherGuard(deny) },
		"prepare_subscriber": func(r *Router) {
			r.mu.Lock()
			r.publisherGuard = deny
			r.mu.Unlock()
			r.PrepareSubscriber("b")
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := New(testLogger(), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()
			r := NewRouter(nil)
			attachFakePeer(t, e, r, "a")
			attachFakePeer(t, e, r, "b")
			r.JoinChannel(1, "a")
			r.JoinChannel(1, "b")
			if pubTrackFor(r, "b", "a") == nil {
				t.Fatal("missing pair b <- a")
			}
			r.mu.RLock()
			peer := r.pubPeers["b"]
			r.mu.RUnlock()

			// Stall b's sender removal, as a long SetRemoteDescription would.
			peer.removalMu.Lock()
			done := make(chan struct{})
			go func() { prune(r); close(done) }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				if r.mu.TryRLock() {
					_, present := r.pubTracks["b"]["a"]
					r.mu.RUnlock()
					if !present {
						break
					}
				}
				if time.Now().After(deadline) {
					peer.removalMu.Unlock()
					<-done
					t.Fatal("Router.mu stayed held while a pruned sender was being removed")
				}
				time.Sleep(time.Millisecond)
			}
			peer.removalMu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("prune did not finish after the sender removal was released")
			}
			r.mu.RLock()
			pending := len(r.trackRemovals)
			r.mu.RUnlock()
			if pending != 0 {
				t.Fatalf("%d track removals left queued", pending)
			}
		})
	}
}
