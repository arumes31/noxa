package server

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"voicx/internal/netproto"
)

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
