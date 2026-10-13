package server

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"noxa/internal/netproto"
)

type closeDiagnosticWriteConn struct {
	net.Conn
	failure error
}

func (c closeDiagnosticWriteConn) Write([]byte) (int, error) { return 0, c.failure }

func TestControlWriteDiagnosticDoesNotExposeConnectionData(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		reason  string
	}{
		{"timeout", &net.OpError{Op: "secret-operation", Net: "secret-network", Err: os.ErrDeadlineExceeded}, "write_timeout"},
		{"error", errors.New("secret endpoint/token/payload"), "write_error"},
		{"closed", net.ErrClosed, "local_close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, observed := observer.New(zap.DebugLevel)
			s := &TCPServer{logger: zap.New(core)}
			client := &Client{ID: "private-session-id", Conn: closeDiagnosticWriteConn{failure: tc.failure}}
			frame := &netproto.Frame{Type: uint16(netproto.MsgPing), Payload: []byte("private-payload")}
			if err := s.writeFrameLocked(context.Background(), client, frame); !errors.Is(err, tc.failure) {
				t.Fatal("write error changed")
			}
			entries := observed.All()
			if len(entries) != 1 || entries[0].Message != "write error" {
				t.Fatal("missing single write diagnostic")
			}
			fields := entries[0].ContextMap()
			if len(fields) != 2 || fields["reason"] != tc.reason || fields["msg_type"] != netproto.MsgPing.String() {
				t.Fatalf("unexpected diagnostic fields: %v", fields)
			}
		})
	}
}
