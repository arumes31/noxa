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

// mediaSocketShape lets chaos tests reorder or delay datagrams on the shared
// media socket: it receives each datagram, its destination and a write
// function for it and decides when (and in which order) to write. Production
// never installs it.
var mediaSocketShape atomic.Pointer[func(datagram []byte, addr net.Addr, write func([]byte) error) error]

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
	if shape := mediaSocketShape.Load(); shape != nil {
		err := (*shape)(b, addr, func(d []byte) error { _, err := c.PacketConn.WriteTo(d, addr); return err })
		if err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, addr)
}
