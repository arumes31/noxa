package broadcast

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"noxa/internal/state"
)

func newTestBroadcaster(t *testing.T) (*Broadcaster, *state.Manager) {
	t.Helper()
	sm := state.New(zap.NewNop())
	return New(zap.NewNop(), sm), sm
}

func TestRegisterUnregisterLifecycle(t *testing.T) {
	b, _ := newTestBroadcaster(t)
	defer b.Close()

	ch, err := b.Register("c1")
	if err != nil {
		t.Fatalf("Register err = %v", err)
	}
	if ch == nil {
		t.Fatal("Register returned nil channel")
	}
	if b.ClientCount() != 1 {
		t.Fatalf("ClientCount = %d, want 1", b.ClientCount())
	}

	b.Unregister("c1")
	if b.ClientCount() != 0 {
		t.Fatalf("ClientCount after Unregister = %d, want 0", b.ClientCount())
	}

	// Unregister unknown is a no-op.
	b.Unregister("does-not-exist")
}

func TestRegisterTwiceError(t *testing.T) {
	b, _ := newTestBroadcaster(t)
	defer b.Close()

	if _, err := b.Register("c1"); err != nil {
		t.Fatalf("first Register err = %v", err)
	}
	if _, err := b.Register("c1"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register err = %v, want ErrAlreadyRegistered", err)
	}
}

func TestBroadcastSnapshotDeliversToAll(t *testing.T) {
	b, sm := newTestBroadcaster(t)
	defer b.Close()

	// Populate state so the snapshot has content.
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "root", CreatedAt: time.Now()})

	ch1, _ := b.Register("c1")
	ch2, _ := b.Register("c2")

	b.BroadcastSnapshot(false, "")

	for i, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case msg := <-ch:
			if !strings.Contains(string(msg), "root_channels") {
				t.Fatalf("client %d got invalid snapshot JSON: %s", i, string(msg))
			}
		case <-time.After(time.Second):
			t.Fatalf("client %d did not receive snapshot", i)
		}
	}
}

func TestTelemetryObserversRunAfterBroadcastLocks(t *testing.T) {
	sm := state.New(zap.NewNop())
	var broadcaster *Broadcaster
	snapshotDuration := make(chan time.Duration, 1)
	backlogDepth := make(chan int, 1)
	var unregisterOnce sync.Once
	broadcaster = New(zap.NewNop(), sm, Observers{
		ObserveSnapshotDuration: func(duration time.Duration) {
			_ = broadcaster.Stats()
			snapshotDuration <- duration
		},
		ObserveClientBacklog: func(depth int) {
			// Re-enter a write-locked operation. If telemetry ran under mu or
			// sendMu this blocks the broadcast rather than completing below.
			unregisterOnce.Do(func() { broadcaster.Unregister("c1") })
			backlogDepth <- depth
		},
	})
	defer broadcaster.Close()
	if _, err := broadcaster.Register("c1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	done := make(chan struct{})
	go func() {
		broadcaster.BroadcastSnapshot(false, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked while invoking telemetry observer")
	}
	select {
	case duration := <-snapshotDuration:
		if duration < 0 {
			t.Fatalf("snapshot duration = %s, want non-negative", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot observer was not called")
	}
	if extra := len(snapshotDuration); extra != 0 {
		t.Fatalf("snapshot observer calls = %d, want exactly 1", extra+1)
	}
	select {
	case depth := <-backlogDepth:
		if depth < 0 || depth > clientBufferSize {
			t.Fatalf("backlog depth = %d, want 0..%d", depth, clientBufferSize)
		}
	case <-time.After(time.Second):
		t.Fatal("backlog observer was not called")
	}
}

func TestBacklogObserverReportsFullQueueDepth(t *testing.T) {
	var depths []int
	var depthsMu sync.Mutex
	b := New(zap.NewNop(), state.New(zap.NewNop()), Observers{
		ObserveClientBacklog: func(depth int) {
			depthsMu.Lock()
			depths = append(depths, depth)
			depthsMu.Unlock()
		},
	})
	defer b.Close()
	if _, err := b.Register("c1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for range clientBufferSize {
		if err := b.BroadcastToClient("c1", []byte("fill")); err != nil {
			t.Fatalf("fill queue: %v", err)
		}
	}
	if err := b.BroadcastToClient("c1", []byte("full")); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("full queue error = %v, want ErrChannelFull", err)
	}
	depthsMu.Lock()
	defer depthsMu.Unlock()
	if len(depths) != clientBufferSize+1 {
		t.Fatalf("backlog observations = %d, want %d", len(depths), clientBufferSize+1)
	}
	if got := depths[len(depths)-1]; got != clientBufferSize {
		t.Fatalf("full queue backlog depth = %d, want %d", got, clientBufferSize)
	}
}

// TestBroadcastSnapshotHidesInvisible verifies the viewer/admin flags reach
// BuildSnapshot: a fan-out must not leak invisible users (381).
func TestBroadcastSnapshotHidesInvisible(t *testing.T) {
	b, sm := newTestBroadcaster(t)
	defer b.Close()

	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "root", CreatedAt: time.Now()})
	sm.AddClient(&state.Client{ClientID: "g1", UniqueID: "ghost-uid", Nickname: "Ghost", Status: "invisible", ConnectedAt: time.Now()})
	_ = sm.JoinChannel("g1", 1)

	ch, _ := b.Register("c1")
	b.BroadcastSnapshot(false, "")

	select {
	case msg := <-ch:
		if strings.Contains(string(msg), "Ghost") {
			t.Fatalf("invisible user leaked into the fan-out snapshot: %s", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive snapshot")
	}
}

func TestBroadcastToChannelScoped(t *testing.T) {
	b, sm := newTestBroadcaster(t)
	defer b.Close()

	sm.AddChannel(&state.Channel{ChannelID: 10, Name: "ch10", CreatedAt: time.Now()})
	sm.AddChannel(&state.Channel{ChannelID: 20, Name: "ch20", CreatedAt: time.Now()})

	c1 := &state.Client{ClientID: "c1", ConnectedAt: time.Now()}
	c2 := &state.Client{ClientID: "c2", ConnectedAt: time.Now()}
	sm.AddClient(c1)
	sm.AddClient(c2)
	_ = sm.JoinChannel("c1", 10)
	_ = sm.JoinChannel("c2", 20)

	ch1, _ := b.Register("c1")
	ch2, _ := b.Register("c2")

	payload := []byte("hello-ch10")
	b.BroadcastToChannel(10, payload)

	select {
	case msg := <-ch1:
		if string(msg) != "hello-ch10" {
			t.Fatalf("c1 got %q, want hello-ch10", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("c1 did not receive channel broadcast")
	}

	select {
	case msg := <-ch2:
		t.Fatalf("c2 should not receive ch10 broadcast, got %q", string(msg))
	case <-time.After(100 * time.Millisecond):
		// expected: no message
	}
}

func TestBroadcastToClient(t *testing.T) {
	b, _ := newTestBroadcaster(t)
	defer b.Close()

	ch, err := b.Register("c1")
	if err != nil {
		t.Fatalf("Register err = %v", err)
	}

	payload := []byte("direct")
	if err := b.BroadcastToClient("c1", payload); err != nil {
		t.Fatalf("BroadcastToClient err = %v", err)
	}
	select {
	case msg := <-ch:
		if string(msg) != "direct" {
			t.Fatalf("got %q, want direct", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive direct message")
	}

	// Unregistered client returns ErrNotRegistered.
	if err := b.BroadcastToClient("ghost", payload); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("BroadcastToClient unknown err = %v, want ErrNotRegistered", err)
	}
}

func TestBroadcastToClientFullChannelDrops(t *testing.T) {
	b, _ := newTestBroadcaster(t)
	defer b.Close()

	ch, _ := b.Register("c1")

	// Fill the buffer (clientBufferSize messages).
	for i := 0; i < clientBufferSize; i++ {
		if err := b.BroadcastToClient("c1", []byte("fill")); err != nil {
			t.Fatalf("fill %d err = %v", i, err)
		}
	}

	// The (N+1)th send must return ErrChannelFull (non-blocking drop).
	if err := b.BroadcastToClient("c1", []byte("overflow")); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("overflow err = %v, want ErrChannelFull", err)
	}

	// Drain and verify we got exactly clientBufferSize messages.
	count := 0
	drain := time.After(time.Second)
loop:
	for {
		select {
		case <-ch:
			count++
		case <-drain:
			break loop
		}
	}
	if count != clientBufferSize {
		t.Fatalf("drained %d messages, want %d", count, clientBufferSize)
	}
	stats := b.Stats()
	if stats.Delivered != clientBufferSize || stats.Dropped != 1 || stats.Clients != 1 {
		t.Fatalf("broadcast stats = %+v", stats)
	}
}

func TestBroadcastPayloadOwnership(t *testing.T) {
	b, sm := newTestBroadcaster(t)
	defer b.Close()
	sm.AddChannel(&state.Channel{ChannelID: 10, Name: "shared", CreatedAt: time.Now()})
	for _, id := range []string{"c1", "c2"} {
		sm.AddClient(&state.Client{ClientID: id, ConnectedAt: time.Now()})
		if err := sm.JoinChannel(id, 10); err != nil {
			t.Fatalf("JoinChannel(%s): %v", id, err)
		}
	}
	first, _ := b.Register("c1")
	second, _ := b.Register("c2")
	payload := []byte("immutable")
	b.BroadcastToChannel(10, payload)
	payload[0] = 'X'

	firstPayload := <-first
	firstPayload[1] = 'Y'
	if got := string(<-second); got != "immutable" {
		t.Fatalf("second client payload was aliased: %q", got)
	}
}

func TestConcurrentBroadcastsHaveConsistentRecipientOrder(t *testing.T) {
	b, sm := newTestBroadcaster(t)
	defer b.Close()
	sm.AddChannel(&state.Channel{ChannelID: 10, Name: "shared", CreatedAt: time.Now()})
	for _, id := range []string{"c1", "c2"} {
		sm.AddClient(&state.Client{ClientID: id, ConnectedAt: time.Now()})
		if err := sm.JoinChannel(id, 10); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := b.Register("c1")
	second, _ := b.Register("c2")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < clientBufferSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			b.BroadcastToChannel(10, []byte(fmt.Sprintf("event-%02d", id)))
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < clientBufferSize; i++ {
		one, two := string(<-first), string(<-second)
		if one != two {
			t.Fatalf("recipient order diverged at %d: %q != %q", i, one, two)
		}
	}
}

func TestDropWarningsAreLogarithmicallyBounded(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	b := New(zap.New(core), state.New(zap.NewNop()))
	defer b.Close()
	_, _ = b.Register("stalled")
	for i := 0; i < clientBufferSize+17; i++ {
		_ = b.BroadcastToClient("stalled", []byte("message"))
	}

	const wantWarnings = 5 // client drop counts 1, 2, 4, 8, and 16.
	if got := observed.FilterMessage("broadcast: client channel full, dropping message").Len(); got != wantWarnings {
		t.Fatalf("drop warning count = %d, want %d", got, wantWarnings)
	}
	if got := b.Stats().Dropped; got != 17 {
		t.Fatalf("drop metric = %d, want 17", got)
	}
}

func TestBroadcastEventEnvelope(t *testing.T) {
	b, _ := newTestBroadcaster(t)
	defer b.Close()

	ch, _ := b.Register("c1")

	b.BroadcastEvent("user_joined", []byte(`{"client_id":"c2"}`))

	select {
	case msg := <-ch:
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.Type != "user_joined" {
			t.Fatalf("envelope type = %q, want user_joined", env.Type)
		}
		if strings.TrimSpace(string(env.Data)) != `{"client_id":"c2"}` {
			t.Fatalf("envelope data = %q, want client_id payload", string(env.Data))
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive event")
	}
}

func TestCloseClearsRegistry(t *testing.T) {
	b, _ := newTestBroadcaster(t)

	ch, _ := b.Register("c1")
	b.Close()

	if b.ClientCount() != 0 {
		t.Fatalf("ClientCount after Close = %d, want 0", b.ClientCount())
	}

	// Channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed after Close")
		}
	default:
		t.Fatal("channel should be closed (received value or zero)")
	}

	// Register after close should error.
	if _, err := b.Register("c2"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Register after Close error = %v, want ErrClosed", err)
	}
	if err := b.BroadcastToClient("c1", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("BroadcastToClient after Close error = %v, want ErrClosed", err)
	}
}

func shortOverflowGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	old := overflowGrace
	overflowGrace = grace
	t.Cleanup(func() { overflowGrace = old })
}

func watchHandlers(b *Broadcaster) (overflows, resyncs chan string) {
	overflows, resyncs = make(chan string, 4), make(chan string, 4)
	b.SetOverflowHandler(func(id string) { overflows <- id })
	// Events named "chat" stand for those a resync cannot replace.
	b.SetResyncHandler(func(id string) { resyncs <- id }, func(_ string, payload []byte) bool {
		return !strings.Contains(string(payload), `"chat"`)
	})
	return overflows, resyncs
}

func drain(ch <-chan []byte) {
	for len(ch) > 0 {
		<-ch
	}
}

func expectCall(t *testing.T, calls chan string, want, what string) {
	t.Helper()
	select {
	case id := <-calls:
		if id != want {
			t.Fatalf("%s reported for %q, want %q", what, id, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s handler not called", what)
	}
}

func expectNoCall(t *testing.T, calls chan string, wait time.Duration, what string) {
	t.Helper()
	select {
	case id := <-calls:
		t.Fatalf("unexpected %s for %q", what, id)
	case <-time.After(wait):
	}
}

// A client whose writer takes no event from its full queue is stuck and has
// missed events: the overflow handler is told once, so the server can
// disconnect it and let it resync.
func TestOverflowHandlerRunsOncePerStuckClient(t *testing.T) {
	shortOverflowGrace(t, 50*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	overflows, resyncs := watchHandlers(b)
	if _, err := b.Register("slow"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	expectCall(t, overflows, "slow", "overflow")
	for i := 0; i < 64; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	expectNoCall(t, overflows, 200*time.Millisecond, "second overflow")
	expectNoCall(t, resyncs, 10*time.Millisecond, "resync")
}

// A burst fills a healthy client's queue too. It drains, so its drop is
// replaced by a resync instead of a disconnect.
func TestDrainedClientIsResyncedNotClosed(t *testing.T) {
	shortOverflowGrace(t, 50*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	overflows, resyncs := watchHandlers(b)
	ch, err := b.Register("healthy")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	if got := b.Stats().Dropped; got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	drain(ch)
	expectCall(t, resyncs, "healthy", "resync")
	expectNoCall(t, overflows, 100*time.Millisecond, "overflow")
	// The watch ended, so a later drop is watched (and resynced) again.
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	drain(ch)
	expectCall(t, resyncs, "healthy", "second resync")
}

// A client that drains and is refilled within the grace period is full at
// the check but its writer moved: it is watched again, not closed.
func TestRefilledQueueThatMovedIsNotClosed(t *testing.T) {
	shortOverflowGrace(t, 300*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	overflows, resyncs := watchHandlers(b)
	ch, err := b.Register("busy")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	drain(ch)
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	expectNoCall(t, overflows, 500*time.Millisecond, "overflow")
	expectNoCall(t, resyncs, 10*time.Millisecond, "resync while full")
	drain(ch)
	expectCall(t, resyncs, "busy", "resync")
	expectNoCall(t, overflows, 400*time.Millisecond, "overflow")
}

// A queue that its writer drains but producers refill before every check
// never has room: it is behind, and is closed after overflowRounds periods
// rather than missing events forever.
func TestFullQueueThatKeepsMovingIsClosed(t *testing.T) {
	shortOverflowGrace(t, 50*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	overflows, resyncs := watchHandlers(b)
	ch, err := b.Register("behind")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	b.mu.RLock()
	queue := b.clients["behind"]
	b.mu.RUnlock()
	stop := make(chan struct{})
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				// Take one and refill under sendMu, so no check sees room.
				b.sendMu.Lock()
				b.mu.RLock()
				<-ch
				b.trySend(queue, "behind", []byte(`{}`))
				b.trySend(queue, "behind", []byte(`{}`))
				b.mu.RUnlock()
				b.sendMu.Unlock()
			}
		}
	}()
	expectCall(t, overflows, "behind", "overflow")
	close(stop)
	<-pumped
	expectNoCall(t, resyncs, 10*time.Millisecond, "resync")
}

// A dropped event that a snapshot cannot replace (chat) is not resynced: the
// client is closed so it reconnects and refetches it.
func TestLossyDropClosesInsteadOfResync(t *testing.T) {
	shortOverflowGrace(t, 50*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	overflows, resyncs := watchHandlers(b)
	ch, err := b.Register("chatty")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < clientBufferSize; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	b.BroadcastEvent("chat", []byte(`{}`))
	drain(ch)
	expectCall(t, overflows, "chatty", "overflow")
	expectNoCall(t, resyncs, 10*time.Millisecond, "resync")
}

// A watch ends when its queue is unregistered, even while the closed queue
// stays full and no overflow handler is set.
func TestWatchEndsWhenQueueUnregistered(t *testing.T) {
	shortOverflowGrace(t, 20*time.Millisecond)
	b, _ := newTestBroadcaster(t)
	resyncs := make(chan string, 4)
	b.SetResyncHandler(func(id string) { resyncs <- id }, func(string, []byte) bool { return true })
	before := runtime.NumGoroutine()
	if _, err := b.Register("gone"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < clientBufferSize+1; i++ {
		b.BroadcastEvent("speaking_changed", []byte(`{}`))
	}
	if runtime.NumGoroutine() <= before {
		t.Fatal("no watch started for the drop")
	}
	b.Unregister("gone")
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("watch still running after Unregister: %d goroutines, want %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
	expectNoCall(t, resyncs, 10*time.Millisecond, "resync")
}
