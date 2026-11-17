package server

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/config"
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
// passes without closing the client, and only written rows are marked. A
// login replays at most spoolDeliveryPasses passes.
func TestDeliverSpooledWritesBacklogDirectly(t *testing.T) {
	const capped = spoolDeliveryPasses * spoolDeliveryBudget
	for _, tc := range []struct {
		name             string
		backlog, written int
		limit            int64
	}{
		{"healthy", spoolDeliveryBudget + 88, spoolDeliveryBudget + 88, 1 << 30},
		{"breaks", spoolDeliveryBudget + 88, 20, 20},
		{"bounded", capped + 10, capped, 1 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backlog := tc.backlog
			spool := &fakeSpool{}
			for range backlog {
				if err := spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed"); err != nil {
					t.Fatal(err)
				}
			}
			s := New(&config.Config{}, zap.NewNop(), &Deps{Spool: spool})
			conn := &frameCountingConn{limit: tc.limit}
			s.deliverSpooled(t.Context(), &Client{ID: "c", Conn: conn}, 2)
			if got := spool.pendingCount(); got != backlog-tc.written {
				t.Fatalf("pending = %d, want %d", got, backlog-tc.written)
			}
			if conn.closed.Load() != (tc.limit < int64(backlog)) {
				t.Fatalf("client closed = %v", conn.closed.Load())
			}
		})
	}
}
