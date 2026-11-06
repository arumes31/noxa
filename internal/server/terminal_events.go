package server

import (
	"context"
	"time"

	"go.uber.org/zap"

	"noxa/internal/netproto"
)

// eventShutdownScheduled announces an orderly shutdown in Seconds. Older
// clients ignore unknown events; the final server_shutdown still follows.
const eventShutdownScheduled = "server_shutdown_scheduled"

type shutdownScheduledEvent struct {
	Seconds int `json:"seconds"`
}

// AnnounceShutdown tells every connected session that the server stops in
// after, and remembers the deadline so sessions that log in later are told
// too. It does not change the lifecycle; Shutdown remains the only stop.
func (s *TCPServer) AnnounceShutdown(after time.Duration) {
	seconds := shutdownSeconds(after)
	if seconds < 1 {
		return
	}
	s.shutdownAt.Store(time.Now().Add(after).UnixNano())
	s.logger.Info("announcing scheduled shutdown", zap.Int("seconds", seconds))
	s.broadcastEvent(eventShutdownScheduled, shutdownScheduledEvent{Seconds: seconds})
}

func shutdownSeconds(after time.Duration) int {
	return int((after + time.Second - 1) / time.Second)
}

// pendingShutdownEvent is the countdown for a session that logs in while a
// shutdown is scheduled; ok is false when none is pending.
func (s *TCPServer) pendingShutdownEvent(now time.Time) ([]byte, bool) {
	at := s.shutdownAt.Load()
	if at == 0 {
		return nil, false
	}
	seconds := shutdownSeconds(time.Unix(0, at).Sub(now))
	if seconds < 1 {
		return nil, false
	}
	payload, err := eventEnvelope(eventShutdownScheduled, shutdownScheduledEvent{Seconds: seconds})
	return payload, err == nil
}

// sendTerminalEvent writes directly before closing a connection. A broadcast
// queue alone can lose the reason when teardown wins the writer race. Keep
// this best-effort write bounded; a stalled peer must never delay removal.
func (s *TCPServer) sendTerminalEvent(client *Client, event string, data any) {
	s.sendTerminalEventInContext(context.Background(), client, event, data)
}

func (s *TCPServer) sendTerminalEventInContext(ctx context.Context, client *Client, event string, data any) {
	if ctx.Err() != nil {
		return
	}
	if !client.wmu.TryLock() {
		return
	}
	defer client.wmu.Unlock()
	payload, err := eventEnvelope(event, data)
	if err != nil {
		return
	}
	deadline := time.Now().Add(150 * time.Millisecond)
	if remaining, ok := ctx.Deadline(); ok && remaining.Before(deadline) {
		deadline = remaining
	}
	if err := client.Conn.SetWriteDeadline(deadline); err != nil {
		return
	}
	defer func() {
		// The connection is about to close; resetting its deadline is best effort.
		_ = client.Conn.SetWriteDeadline(time.Time{})
	}()
	_ = netproto.WriteFrame(client.Conn, &netproto.Frame{Type: uint16(netproto.MsgEvent), Payload: payload})
}
