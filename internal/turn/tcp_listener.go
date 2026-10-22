package turn

import (
	"net"
	"sync"
	"time"

	pion "github.com/pion/turn/v5"
	"golang.org/x/net/netutil"
)

// Unauthenticated streams have a fixed lifetime, a separate global budget and
// a per-source budget. Trickle bytes never extend the authentication deadline.
type tcpListener struct {
	net.Listener
	mu              sync.Mutex
	closed          bool
	conns           map[string]*tcpConn
	pending         int
	pendingByIP     map[string]int
	preAuthLifetime time.Duration
}

func limitedTCPListener(listener net.Listener, maximum int) *tcpListener {
	return &tcpListener{Listener: netutil.LimitListener(listener, min(512, maximum)), conns: make(map[string]*tcpConn), pendingByIP: make(map[string]int), preAuthLifetime: 30 * time.Second}
}
func (l *tcpListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		if err != nil || l.pending >= 64 || l.pendingByIP[ip] >= 8 {
			l.mu.Unlock()
			_ = conn.Close()
			continue
		}
		key := conn.RemoteAddr().String()
		wrapped := &tcpConn{Conn: conn, owner: l, key: key, ip: ip}
		l.conns[key] = wrapped
		l.pending++
		l.pendingByIP[ip]++
		wrapped.timer = time.AfterFunc(l.preAuthLifetime, func() {
			l.mu.Lock()
			pending := l.conns[key] == wrapped && wrapped.grant == ""
			l.mu.Unlock()
			if pending {
				_ = wrapped.Close()
			}
		})
		l.mu.Unlock()
		return wrapped, nil
	}
}

// Only a successful allocation admits a TCP stream. AuthHandler is a key lookup
// before MESSAGE-INTEGRITY verification and cannot establish authentication.
func (l *tcpListener) events() pion.EventHandler {
	return pion.EventHandler{
		OnAllocationCreated: func(src, _ net.Addr, _, userID, _ string, _ net.Addr, _ int) {
			if _, ok := src.(*net.TCPAddr); !ok {
				return
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			if c := l.conns[src.String()]; c != nil && c.grant == "" {
				c.grant = userID
				c.timer.Stop()
				l.releasePending(c.ip)
			}
		},
		OnAllocationDeleted: func(src, _ net.Addr, _, userID, _ string) {
			if _, ok := src.(*net.TCPAddr); !ok {
				return
			}
			l.mu.Lock()
			c := l.conns[src.String()]
			closeConn := c != nil && c.grant == userID
			l.mu.Unlock()
			if closeConn {
				_ = c.Close()
			}
		},
	}
}
func (l *tcpListener) releasePending(ip string) {
	l.pending--
	l.pendingByIP[ip]--
	if l.pendingByIP[ip] == 0 {
		delete(l.pendingByIP, ip)
	}
}
func (l *tcpListener) Close() error {
	l.mu.Lock()
	l.closed = true
	conns := make([]*tcpConn, 0, len(l.conns))
	for _, conn := range l.conns {
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
	owner          *tcpListener
	key, ip, grant string // owner.mu protects grant and timer lifecycle
	timer          *time.Timer
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
	if c.owner.conns[c.key] == c {
		delete(c.owner.conns, c.key)
		c.timer.Stop()
		if c.grant == "" {
			c.owner.releasePending(c.ip)
		}
	}
	c.owner.mu.Unlock()
	return c.Conn.Close()
}
