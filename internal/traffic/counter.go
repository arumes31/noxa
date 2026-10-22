// Package traffic counts bytes in RAM; socket operations never touch storage.
package traffic

import (
	"net"
	"sync/atomic"
)

type Counts struct {
	In  int64 `json:"in_bytes"`
	Out int64 `json:"out_bytes"`
}

func (c Counts) Add(v Counts) Counts { return Counts{c.In + v.In, c.Out + v.Out} }
func (c Counts) Sub(v Counts) Counts { return Counts{max(0, c.In-v.In), max(0, c.Out-v.Out)} }

type Counter struct {
	incoming atomic.Int64
	outgoing atomic.Int64
}

func (c *Counter) In(n int) {
	if c != nil && n > 0 {
		c.incoming.Add(int64(n))
	}
}
func (c *Counter) Out(n int) {
	if c != nil && n > 0 {
		c.outgoing.Add(int64(n))
	}
}
func (c *Counter) Snapshot() Counts {
	if c == nil {
		return Counts{}
	}
	return Counts{c.incoming.Load(), c.outgoing.Load()}
}

type countedConn struct {
	net.Conn
	counter *Counter
}

func (c *countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.counter.In(n)
	return n, err
}
func (c *countedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.counter.Out(n)
	return n, err
}
func WrapConn(conn net.Conn, counter *Counter) net.Conn {
	if counter == nil {
		return conn
	}
	return &countedConn{conn, counter}
}

type countedListener struct {
	net.Listener
	counter *Counter
}

func (l *countedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return WrapConn(conn, l.counter), nil
}
func WrapListener(listener net.Listener, counter *Counter) net.Listener {
	if counter == nil {
		return listener
	}
	return &countedListener{listener, counter}
}

type countedPacketConn struct {
	net.PacketConn
	counter *Counter
}

func (c *countedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.counter.In(n)
	return n, addr, err
}
func (c *countedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	c.counter.Out(n)
	return n, err
}
func WrapPacketConn(conn net.PacketConn, counter *Counter) net.PacketConn {
	if counter == nil {
		return conn
	}
	return &countedPacketConn{conn, counter}
}
