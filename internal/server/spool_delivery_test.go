package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/store"
)

// frameCountingConn accepts limit frames (header and payload writes) and
// then fails, like a peer whose connection broke mid-backlog.
type frameCountingConn struct {
	net.Conn
	limit  int64
	frames atomic.Int64
	closed atomic.Bool
}

func (c *frameCountingConn) Write(p []byte) (int, error) {
	frames := c.frames.Load()
	if len(p) == 6 { // frame header; spooled chat payloads are far longer
		frames = c.frames.Add(1)
	}
	if frames > c.limit {
		return 0, errors.New("broken pipe")
	}
	return len(p), nil
}
func (c *frameCountingConn) SetWriteDeadline(time.Time) error { return nil }
func (c *frameCountingConn) Close() error                     { c.closed.Store(true); return nil }

// A backlog far beyond the 16-slot broadcast queue is delivered in budgeted
// passes without closing the client. A login replays at most
// spoolDeliveryPasses passes. Rows the client acknowledges (acked) stay
// pending until its receipt; rows without a client_msg_id are marked when
// written.
func TestDeliverSpooledWritesBacklogDirectly(t *testing.T) {
	const capped = spoolDeliveryPasses * spoolDeliveryBudget
	for _, tc := range []struct {
		name             string
		backlog, written int
		limit            int64
		acked            bool
	}{
		{"healthy", spoolDeliveryBudget + 88, spoolDeliveryBudget + 88, 1 << 30, false},
		{"breaks", spoolDeliveryBudget + 88, 20, 20, false},
		{"bounded", capped + 10, capped, 1 << 30, false},
		{"healthy awaiting receipts", spoolDeliveryBudget + 88, spoolDeliveryBudget + 88, 1 << 30, true},
		{"breaks awaiting receipts", spoolDeliveryBudget + 88, 20, 20, true},
		{"bounded awaiting receipts", capped + 10, capped, 1 << 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backlog := tc.backlog
			spool := &fakeSpool{}
			for i := range backlog {
				var binding []store.DMKeyBinding
				if tc.acked {
					binding = []store.DMKeyBinding{{ClientMsgID: strconv.Itoa(i)}}
				}
				if err := spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed", binding...); err != nil {
					t.Fatal(err)
				}
			}
			s := New(&config.Config{}, zap.NewNop(), &Deps{Spool: spool})
			conn := &frameCountingConn{limit: tc.limit}
			s.deliverSpooled(t.Context(), &Client{ID: "c", Conn: conn}, 2)
			if got := int(min(conn.frames.Load(), tc.limit)); got != tc.written {
				t.Fatalf("frames written = %d, want %d", got, tc.written)
			}
			want := backlog - tc.written
			if tc.acked {
				want = backlog // a write alone marks nothing
			}
			if got := spool.pendingCount(); got != want {
				t.Fatalf("pending = %d, want %d", got, want)
			}
			if conn.closed.Load() != (tc.limit < int64(backlog)) {
				t.Fatalf("client closed = %v", conn.closed.Load())
			}
		})
	}
}

// A message that was written but never acknowledged (the connection died
// before the client read it) replays on the next session; a receipt marks
// exactly the acknowledged message; one session is not sent a message twice.
func TestSpooledMessageReplaysUntilAcknowledged(t *testing.T) {
	spool := &fakeSpool{}
	for _, m := range []struct {
		from int64
		id   string
	}{{1, "m1"}, {1, "m2"}, {1, ""}, {2, "own"}} {
		if err := spool.SpoolMessage(t.Context(), m.from, 2, "admin-uid", "sealed", store.DMKeyBinding{ClientMsgID: m.id}); err != nil {
			t.Fatal(err)
		}
	}
	s := New(&config.Config{}, zap.NewNop(), &Deps{Spool: spool})
	firstConn := &frameCountingConn{limit: 1 << 30}
	first := &Client{ID: "first", Conn: firstConn}
	s.deliverSpooled(t.Context(), first, 2)
	// The row without an id and the account's own copy get no receipt: they
	// are marked when written.
	if frames, pending := firstConn.frames.Load(), spool.pendingCount(); frames != 4 || pending != 2 {
		t.Fatalf("first login: frames = %d, pending = %d; want 4 and 2", frames, pending)
	}
	// A key publish in the same session does not send them again.
	s.deliverSpooled(t.Context(), first, 2)
	if frames := firstConn.frames.Load(); frames != 4 {
		t.Fatalf("frames after a second pass in one session = %d, want 4", frames)
	}
	s.ackSpooled(t.Context(), first, "someone-else", "m2")
	s.ackSpooled(t.Context(), first, "admin-uid", "m1")
	if pending := spool.pendingCount(); pending != 1 {
		t.Fatalf("pending after the receipt for m1 = %d, want 1", pending)
	}
	// No receipt for m2: the next session gets it again, and it is marked.
	secondConn := &frameCountingConn{limit: 1 << 30}
	s.deliverSpooled(t.Context(), &Client{ID: "second", Conn: secondConn}, 2)
	if frames, pending := secondConn.frames.Load(), spool.pendingCount(); frames != 1 || pending != 0 {
		t.Fatalf("reconnect: frames = %d, pending = %d; want 1 and 0", frames, pending)
	}
}

// The spool replay at login holds no policy lease: a policy writer that
// arrives while the replay waits on a slow store or link is not blocked (and
// so does not block every later reader).
func TestSpoolReplayAtLoginHoldsNoPolicyLease(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	spool := &gatedBoundSpool{fakeSpool: env.spool, entered: make(chan struct{}), release: make(chan struct{})}
	env.srv.deps.Spool = spool
	if err := spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed"); err != nil {
		t.Fatal(err)
	}
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	defer close(spool.release)
	select {
	case <-spool.entered:
	case <-time.After(waitDeadline):
		t.Fatal("login never replayed the spool")
	}
	written := make(chan error, 1)
	go func() {
		written <- env.srv.deps.Authority.WithExclusivePolicy(context.Background(), func(*authorization.RoleEvaluator) error { return nil })
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a policy writer waited for the spool replay")
	}
}
