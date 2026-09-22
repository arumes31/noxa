package webrtc

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v4"
)

const mediaSocketWriteTimeout = 250 * time.Millisecond

// socketWriteGate serializes deadline ownership on shared ICE sockets. Waiting
// for another writer consumes the same deadline as the eventual network write.
// Deadline updates can shorten an active write but cannot extend its budget.
type socketWriteGate struct {
	once              sync.Once
	gate              chan struct{}
	mu                sync.Mutex
	active, requested time.Time
}

func (g *socketWriteGate) setDeadline(t time.Time, set func(time.Time) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requested = t
	if !g.active.IsZero() && (t.IsZero() || g.active.Before(t)) {
		t = g.active
	}
	return set(t)
}

func (g *socketWriteGate) write(set func(time.Time) error, write func() (int, error)) (int, error) {
	deadline := time.Now().Add(mediaSocketWriteTimeout)
	g.once.Do(func() { g.gate = make(chan struct{}, 1) })
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case g.gate <- struct{}{}:
	case <-timer.C:
		return 0, os.ErrDeadlineExceeded
	}
	defer func() { <-g.gate }()
	g.mu.Lock()
	if !g.requested.IsZero() && g.requested.Before(deadline) {
		deadline = g.requested
	}
	g.active = deadline
	err := set(deadline)
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active = time.Time{}; g.mu.Unlock() }()
	if err != nil {
		return 0, err
	}
	return write()
}

type boundedPacketConn struct {
	net.PacketConn
	writes socketWriteGate
}

func (c *boundedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.writes.write(c.PacketConn.SetWriteDeadline, func() (int, error) { return c.PacketConn.WriteTo(b, addr) })
}
func (c *boundedPacketConn) SetWriteDeadline(t time.Time) error {
	return c.writes.setDeadline(t, c.PacketConn.SetWriteDeadline)
}
func (c *boundedPacketConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

type boundedUDPConn struct {
	transport.UDPConn
	writes socketWriteGate
}

func (c *boundedUDPConn) Write(b []byte) (int, error) {
	return c.writes.write(c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.Write(b) })
}
func (c *boundedUDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.writes.write(c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.WriteTo(b, addr) })
}
func (c *boundedUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	return c.writes.write(c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.WriteToUDP(b, addr) })
}
func (c *boundedUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	n, err = c.writes.write(c.UDPConn.SetWriteDeadline, func() (int, error) { var e error; n, oobn, e = c.UDPConn.WriteMsgUDP(b, oob, addr); return n, e })
	return n, oobn, err
}
func (c *boundedUDPConn) SetWriteDeadline(t time.Time) error {
	return c.writes.setDeadline(t, c.UDPConn.SetWriteDeadline)
}
func (c *boundedUDPConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

type boundedTCPConn struct {
	transport.TCPConn
	writes socketWriteGate
}

func (c *boundedTCPConn) Write(b []byte) (int, error) {
	return c.writes.write(c.TCPConn.SetWriteDeadline, func() (int, error) { return c.TCPConn.Write(b) })
}
func (c *boundedTCPConn) SetWriteDeadline(t time.Time) error {
	return c.writes.setDeadline(t, c.TCPConn.SetWriteDeadline)
}
func (c *boundedTCPConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// These are the socket constructors used by the pinned ICE implementation for
// direct candidates, STUN and UDP/TCP/TLS TURN. The shared UDP mux is wrapped
// separately because it is constructed outside transport.Net.
type boundedMediaNet struct{ transport.Net }

func (n boundedMediaNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	return &boundedUDPConn{UDPConn: c}, nil
}
func (n boundedMediaNet) ListenPacket(network, addr string) (net.PacketConn, error) {
	c, err := n.Net.ListenPacket(network, addr)
	if err != nil {
		return nil, err
	}
	return &boundedPacketConn{PacketConn: c}, nil
}
func (n boundedMediaNet) DialUDP(network string, local, remote *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.DialUDP(network, local, remote)
	if err != nil {
		return nil, err
	}
	return &boundedUDPConn{UDPConn: c}, nil
}
func (n boundedMediaNet) DialTCP(network string, local, remote *net.TCPAddr) (transport.TCPConn, error) {
	c, err := n.Net.DialTCP(network, local, remote)
	if err != nil {
		return nil, err
	}
	return &boundedTCPConn{TCPConn: c}, nil
}
