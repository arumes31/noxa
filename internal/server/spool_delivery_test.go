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
	limit  int
	frames int
	closed atomic.Bool
}

func (c *frameCountingConn) Write(p []byte) (int, error) {
	if len(p) == 6 { // frame header
		c.frames++
	}
	if c.frames > c.limit {
		return 0, errors.New("broken pipe")
	}
	return len(p), nil
}
func (c *frameCountingConn) SetWriteDeadline(time.Time) error { return nil }
func (c *frameCountingConn) Close() error                     { c.closed.Store(true); return nil }

// A backlog far beyond the 16-slot broadcast queue is delivered in budgeted
// passes without closing the client, and only written rows are marked.
func TestDeliverSpooledWritesBacklogDirectly(t *testing.T) {
	const backlog = spoolDeliveryBudget + 88
	for _, tc := range []struct {
		name  string
		limit int
	}{{"healthy", backlog}, {"breaks", 20}} {
		t.Run(tc.name, func(t *testing.T) {
			spool := &fakeSpool{}
			for range backlog {
				if err := spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed"); err != nil {
					t.Fatal(err)
				}
			}
			s := New(&config.Config{}, zap.NewNop(), &Deps{Spool: spool})
			conn := &frameCountingConn{limit: tc.limit}
			s.deliverSpooled(t.Context(), &Client{ID: "c", Conn: conn}, 2)
			written := min(tc.limit, backlog)
			if got := spool.pendingCount(); got != backlog-written {
				t.Fatalf("pending = %d, want %d (written %d)", got, backlog-written, written)
			}
			if conn.closed.Load() != (tc.limit < backlog) {
				t.Fatalf("client closed = %v", conn.closed.Load())
			}
		})
	}
}
