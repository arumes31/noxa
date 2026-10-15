package webrtc

import (
	"sync"
	"testing"
	"time"
)

// Keep a real final packet write in flight while refreshing metadata. A refresh
// that revokes nothing must not queue a watch writer behind this callback.
func TestPublisherRefreshPreservesInFlightAuthorizedWatch(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		name := "guard"
		if prepare {
			name = "prepare_subscriber"
		}
		t.Run(name, func(t *testing.T) {
			r, delivery := publisherContentionFixture(t)
			// A forbidden but already inactive watch must not require a writer.
			r.watches[watchKey{"old", "pub", SlotScreen}] = videoWatch{active: false}
			guard := PublisherGuard(func(p PublisherAccess) bool { return p.SubscriberID != "old" })
			r.SetPublisherGuard(guard)
			release := holdPublisherPacketWrite(t, r, delivery)
			done := make(chan struct{})
			go func() {
				if prepare {
					r.PrepareSubscriber("unrelated")
				} else {
					r.SetPublisherGuard(guard)
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				release()
				<-done
				t.Fatal("unchanged permissions waited for an in-flight packet write")
			}
			// The refresh also leaves unrelated router readers available.
			if !r.mu.TryRLock() {
				t.Fatal("refresh retained the router writer")
			}
			r.mu.RUnlock()
			called := false
			_ = r.withWatch(delivery, func() error { called = true; return nil })
			if !called {
				t.Fatal("authorized watch lost packet delivery")
			}
		})
	}
}

func TestPublisherRefreshStillWaitsForRevokedWatch(t *testing.T) {
	r, delivery := publisherContentionFixture(t)
	release := holdPublisherPacketWrite(t, r, delivery)
	evaluated, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		r.SetPublisherGuard(func(PublisherAccess) bool {
			once.Do(func() { close(evaluated) })
			return false
		})
		close(done)
	}()
	select {
	case <-evaluated:
	case <-time.After(time.Second):
		// The old implementation does not evaluate until its writer is held.
		// Release on failure so this regression never strands a goroutine.
		release()
		<-done
		t.Fatal("guard was not evaluated while the packet write was in flight")
	}
	select {
	case <-done:
		t.Fatal("revocation returned before the authorized write finished")
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revocation did not finish after the packet write")
	}
	called := false
	_ = r.withWatch(delivery, func() error { called = true; return nil })
	if called {
		t.Fatal("revoked watch still delivered a queued packet")
	}
	r.watchMu.RLock()
	watch := r.watches[watchKey{"sub", "pub", SlotScreen}]
	r.watchMu.RUnlock()
	if watch.active || watch.epoch != delivery.WatchEpoch || watch.publication != delivery.Publication {
		t.Fatal("revocation lost the inactive watch tombstone")
	}
}

func TestPublisherRefreshDoesNotRestoreConcurrentlyRevokedPublication(t *testing.T) {
	r, delivery := publisherContentionFixture(t)
	scanned, resumeScan := make(chan struct{}), make(chan struct{})
	refreshed, revoked := make(chan struct{}), make(chan struct{})
	var releaseOnce, scanOnce sync.Once
	releaseScan := func() { releaseOnce.Do(func() { close(resumeScan) }) }
	revokeStarted := false
	t.Cleanup(func() {
		releaseScan()
		select {
		case <-refreshed:
		case <-time.After(time.Second):
			t.Error("refresh goroutine did not finish")
		}
		if revokeStarted {
			select {
			case <-revoked:
			case <-time.After(time.Second):
				t.Error("publication revocation goroutine did not finish")
			}
		}
	})
	go func() {
		r.SetPublisherGuard(func(PublisherAccess) bool {
			// Pause the first predicate evaluation while the preliminary scan
			// holds a watch reader. No production hook or router re-entry.
			scanOnce.Do(func() { close(scanned); <-resumeScan })
			return false
		})
		close(refreshed)
	}()
	select {
	case <-scanned:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach its read scan")
	}
	revokeStarted = true
	go func() {
		r.RevokeVideo("pub", SlotScreen)
		close(revoked)
	}()
	// A queued writer excludes new TryRLocks. The paused scan still owns a
	// reader, so RevokeVideo cannot finish yet; it has the writer turn before
	// the refresh can leave its scan and request its own writer.
	deadline := time.Now().Add(time.Second)
	for r.watchMu.TryRLock() {
		r.watchMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("RevokeVideo did not queue its watch writer")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-revoked:
		t.Fatal("publication revocation passed the held scan reader")
	default:
	}
	releaseScan()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish after scan release")
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("publication revocation did not finish")
	}
	r.watchMu.RLock()
	_, publicationExists := r.publications[publicationKey{"pub", SlotScreen}]
	_, watchExists := r.watches[watchKey{"sub", "pub", SlotScreen}]
	r.watchMu.RUnlock()
	if publicationExists || watchExists {
		t.Fatal("refresh restored a publication or watch deleted between scan and write")
	}
	called := false
	_ = r.withWatch(delivery, func() error { called = true; return nil })
	if called {
		t.Fatal("a packet from the deleted publication was delivered")
	}
}

func publisherContentionFixture(t *testing.T) (*Router, MediaDelivery) {
	t.Helper()
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	testVideoPublication(t, r, "pub", "sub", SlotScreen)
	delivery := MediaDelivery{SenderID: "pub", RecipientID: "sub", Slot: SlotScreen}
	r.captureWatch(&delivery)
	return r, delivery
}

func holdPublisherPacketWrite(t *testing.T, r *Router, delivery MediaDelivery) func() {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }); <-done }
	t.Cleanup(finish)
	go func() {
		defer close(done)
		_ = r.withWatch(delivery, func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fixture did not enter the packet write")
	}
	return finish
}
