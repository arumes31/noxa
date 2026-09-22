package server

import (
	"context"
	"time"

	"noxa/internal/netproto"
)

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
