package webrtc

import (
	"net"
	"sync/atomic"
)

// mediaSocketFault lets chaos tests fail individual writes on the shared
// media socket the way a socket closed under the in-flight packet (an ICE
// restart) would. It receives the datagram so tests can target SRTP only.
// Production never installs it; the cost is one atomic load per datagram.
var mediaSocketFault atomic.Pointer[func(datagram []byte) error]

// faultInjectingPacketConn sits in front of the shared UDP mux socket and
// consults mediaSocketFault before each write. Every other method is the
// wrapped connection's own.
type faultInjectingPacketConn struct {
	net.PacketConn
}

func (c *faultInjectingPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if fault := mediaSocketFault.Load(); fault != nil {
		if err := (*fault)(b); err != nil {
			return 0, err
		}
	}
	return c.PacketConn.WriteTo(b, addr)
}
