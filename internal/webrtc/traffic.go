package webrtc

import (
	"github.com/pion/transport/v5"
	"net"
	"noxa/internal/traffic"
)

// Embed the complete Pion transport interfaces so socket tuning/deadlines are
// unchanged. Only completed byte counts are added, including partial I/O.
type meteredMediaUDP struct {
	transport.UDPConn
	counter *traffic.Counter
}

func (c *meteredMediaUDP) Read(p []byte) (int, error) {
	n, e := c.UDPConn.Read(p)
	c.counter.In(n)
	return n, e
}
func (c *meteredMediaUDP) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, e := c.UDPConn.ReadFrom(p)
	c.counter.In(n)
	return n, a, e
}
func (c *meteredMediaUDP) ReadFromUDP(p []byte) (int, *net.UDPAddr, error) {
	n, a, e := c.UDPConn.ReadFromUDP(p)
	c.counter.In(n)
	return n, a, e
}
func (c *meteredMediaUDP) ReadMsgUDP(p, o []byte) (int, int, int, *net.UDPAddr, error) {
	n, on, f, a, e := c.UDPConn.ReadMsgUDP(p, o)
	c.counter.In(n)
	return n, on, f, a, e
}
func (c *meteredMediaUDP) Write(p []byte) (int, error) {
	n, e := c.UDPConn.Write(p)
	c.counter.Out(n)
	return n, e
}
func (c *meteredMediaUDP) WriteTo(p []byte, a net.Addr) (int, error) {
	n, e := c.UDPConn.WriteTo(p, a)
	c.counter.Out(n)
	return n, e
}
func (c *meteredMediaUDP) WriteToUDP(p []byte, a *net.UDPAddr) (int, error) {
	n, e := c.UDPConn.WriteToUDP(p, a)
	c.counter.Out(n)
	return n, e
}
func (c *meteredMediaUDP) WriteMsgUDP(p, o []byte, a *net.UDPAddr) (int, int, error) {
	n, on, e := c.UDPConn.WriteMsgUDP(p, o, a)
	c.counter.Out(n)
	return n, on, e
}

type meteredMediaTCP struct {
	transport.TCPConn
	counter *traffic.Counter
}

func (c *meteredMediaTCP) Read(p []byte) (int, error) {
	n, e := c.TCPConn.Read(p)
	c.counter.In(n)
	return n, e
}
func (c *meteredMediaTCP) Write(p []byte) (int, error) {
	n, e := c.TCPConn.Write(p)
	c.counter.Out(n)
	return n, e
}
