package server

import (
	"context"
	"go.uber.org/zap"
	"net"
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestOlmAdmissionDefaultOff(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	s := &TCPServer{logger: zap.NewNop()}
	c := &Client{Conn: server}
	done := make(chan error, 1)
	go func() { done <- s.handleOlmAdmission(t.Context(), c, &netproto.Frame{Payload: []byte(`{}`)}) }()
	f, e := netproto.ReadFrame(peer)
	if e != nil {
		t.Fatal(e)
	}
	if netproto.MessageType(f.Type) != netproto.MsgError {
		t.Fatal("inactive admission was reachable")
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestOlmAdmissionWriteDeadlineLegacyAndQueued(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "socket", true: "mutex"}[queued], func(t *testing.T) {
			server, peer := net.Pipe()
			defer server.Close()
			defer peer.Close()
			s := &TCPServer{logger: zap.NewNop(), olmAdmissionEnabled: true}
			c := &Client{Conn: server}
			if queued {
				c.wmu.Lock()
				defer c.wmu.Unlock()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			e := s.handleOlmAdmission(ctx, c, &netproto.Frame{Payload: []byte(`{}`)})
			if e == nil || time.Since(start) > time.Second {
				t.Fatalf("unbounded write %v", e)
			}
		})
	}
}
