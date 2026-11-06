package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestAnnounceShutdownBroadcastsRoundedSeconds(t *testing.T) {
	b := broadcast.New(zap.NewNop(), state.New(zap.NewNop()))
	var events []string
	b.SetEventTap(func(eventType string, payload []byte) {
		events = append(events, eventType+" "+string(payload))
	})
	s := New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: b})

	s.AnnounceShutdown(29*time.Second + time.Millisecond) // rounds up
	s.AnnounceShutdown(400 * time.Millisecond)            // still at least one second
	s.AnnounceShutdown(0)                                 // nothing left to announce

	want := []string{
		`server_shutdown_scheduled {"seconds":30}`,
		`server_shutdown_scheduled {"seconds":1}`,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, events[i], want[i])
		}
	}
}

// The countdown must survive role-filtered delivery, the path every
// authenticated session's events take; an event type without a policy is
// dropped there.
func TestShutdownScheduledPassesRoleDelivery(t *testing.T) {
	evaluator, err := authorization.NewRoleEvaluator(serverRoleFixture().policy)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := eventEnvelope(eventShutdownScheduled, shutdownScheduledEvent{Seconds: 42})
	if err != nil {
		t.Fatal(err)
	}
	srv := &TCPServer{}
	frame, err := srv.roleBroadcastFrame(&Client{UserID: 1, UniqueID: "member"}, payload, evaluator)
	if err != nil || frame == nil {
		t.Fatalf("frame=%v error=%v; the countdown was dropped", frame, err)
	}
	if !strings.Contains(string(frame.Payload), `"seconds":42`) {
		t.Fatalf("payload = %s", frame.Payload)
	}
}

func TestPendingShutdownEventForLateSessions(t *testing.T) {
	s := New(&config.Config{}, zap.NewNop(), &Deps{})
	now := time.Now()
	if _, ok := s.pendingShutdownEvent(now); ok {
		t.Fatal("countdown reported without an announced shutdown")
	}
	s.shutdownAt.Store(now.Add(25 * time.Second).UnixNano())
	payload, ok := s.pendingShutdownEvent(now)
	if !ok || !strings.Contains(string(payload), `"seconds":25`) || !strings.Contains(string(payload), eventShutdownScheduled) {
		t.Fatalf("payload=%s ok=%v", payload, ok)
	}
	if _, ok := s.pendingShutdownEvent(now.Add(26 * time.Second)); ok {
		t.Fatal("countdown reported after the deadline passed")
	}
}

func TestDispatchCloseReasonSeparatesWriteFailures(t *testing.T) {
	cases := map[error]netproto.ControlCloseReason{
		errors.New("request refused"): netproto.ControlCloseProtocolError,
		net.ErrClosed:                 netproto.ControlCloseLocal,
		&net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}: netproto.ControlCloseWriteTimeout,
		fmt.Errorf("reply: %w", io.ErrClosedPipe):              netproto.ControlCloseWriteError,
	}
	for err, want := range cases {
		if got := dispatchCloseReason(err); got != want {
			t.Errorf("dispatchCloseReason(%v) = %s, want %s", err, got, want)
		}
	}
}
