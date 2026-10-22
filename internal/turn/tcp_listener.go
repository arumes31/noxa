package turn

import (
	"net"
	"sync"
	"time"

	"golang.org/x/net/netutil"
)

// Bound unauthenticated TCP sockets as well as authenticated allocations. Close
// tracks accepted connections because closing net.Listener alone does not.
type tcpListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
	conns  map[*tcpConn]struct{}
}

func limitedTCPListener(listener net.Listener, maximum int) *tcpListener {
	return &tcpListener{Listener: netutil.LimitListener(listener, maximum), conns: make(map[*tcpConn]struct{})}
}
func (l *tcpListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	wrapped := &tcpConn{Conn: conn, owner: l}
	l.conns[wrapped] = struct{}{}
	return wrapped, nil
}
func (l *tcpListener) Close() error {
	l.mu.Lock()
	l.closed = true
	conns := make([]*tcpConn, 0, len(l.conns))
	for conn := range l.conns {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return err
}

type tcpConn struct {
	net.Conn
	owner *tcpListener
}

func (c *tcpConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
func (c *tcpConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
func (c *tcpConn) Close() error {
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}
