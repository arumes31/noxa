package webrtc

import (
	"errors"
	"net"
	"testing"

	"go.uber.org/zap"
)

type receiveBufferSocket struct {
	net.PacketConn
	requested int
	err       error
}

func (s *receiveBufferSocket) SetReadBuffer(size int) error {
	s.requested = size
	return s.err
}

func TestMediaReceiveBufferRequestIsNonfatal(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{{"accepted or clamped", nil}, {"OS rejected", errors.New("buffer unavailable")}} {
		t.Run(test.name, func(t *testing.T) {
			socket := &receiveBufferSocket{err: test.err}
			configureMediaReceiveBuffer(socket, zap.NewNop())
			if socket.requested != 4*1024*1024 {
				t.Fatalf("requested %d bytes", socket.requested)
			}
		})
	}
}
