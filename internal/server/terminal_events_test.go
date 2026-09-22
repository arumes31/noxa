package server

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

type terminalDeadlineConn struct {
	*blockingTCPConn
	deadline time.Time
	writes   int
}

func (c *terminalDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.deadline = deadline
	}
	return nil
}

func (c *terminalDeadlineConn) Write(p []byte) (int, error) { c.writes++; return len(p), nil }

func TestTerminalEventUsesRemainingCleanupBudget(t *testing.T) {
	conn := &terminalDeadlineConn{blockingTCPConn: newBlockingTCPConn()}
	client := &Client{Conn: conn}
	srv := &TCPServer{}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	want, _ := ctx.Deadline()
	srv.sendTerminalEventInContext(ctx, client, "kicked", struct{}{})
	if !conn.deadline.Equal(want) || conn.writes == 0 {
		t.Fatalf("notification renewed deadline: %v instead of %v", conn.deadline, want)
	}
	cancel()
	writes := conn.writes
	srv.sendTerminalEventInContext(ctx, client, "kicked", struct{}{})
	if conn.writes != writes {
		t.Fatal("expired cleanup attempted a notification")
	}
}

func TestTerminalEventDeliveredBeforeClose(t *testing.T) {
	writer, reader := net.Pipe()
	t.Cleanup(func() { _ = writer.Close(); _ = reader.Close() })
	s := &TCPServer{}
	client := &Client{Conn: writer}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sendTerminalEvent(client, "server_shutdown", struct{}{})
		_ = writer.Close()
	}()
	_ = reader.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := netproto.ReadFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(frame.Payload, &event); err != nil {
		t.Fatal(err)
	}
	if frame.Type != uint16(netproto.MsgEvent) || event.Type != "server_shutdown" {
		t.Fatalf("wrong event: %+v", event)
	}
	<-done
}

func TestTerminalEventCannotBlockRemoval(t *testing.T) {
	writer, reader := net.Pipe()
	t.Cleanup(func() { _ = writer.Close(); _ = reader.Close() })
	s := &TCPServer{}
	client := &Client{Conn: writer}
	start := time.Now()
	s.sendTerminalEvent(client, "kicked", kickEvent{ClientID: "self", Ban: true, FromServer: true})
	if time.Since(start) > time.Second {
		t.Fatal("unresponsive peer blocked terminal notification")
	}
	client.wmu.Lock()
	defer client.wmu.Unlock()
	start = time.Now()
	s.sendTerminalEvent(client, "server_shutdown", struct{}{})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("busy writer must not block removal")
	}
}
