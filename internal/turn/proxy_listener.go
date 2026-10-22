package turn

import (
	"bufio"
	"net"
	"sync"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

// proxyListener is exclusively for a TLS-terminating proxy on a private network.
// Parsing is concurrent, bounded and timed so an incomplete header cannot stall
// other users. Public TURN listeners never interpret PROXY headers.
type proxyListener struct {
	net.Listener
	trusted       map[string]bool
	ready         chan net.Conn
	done          chan struct{}
	slots         chan struct{}
	once          sync.Once
	mu            sync.Mutex
	pending       map[net.Conn]struct{}
	closed        bool
	headerTimeout time.Duration
}

func newProxyListener(listener net.Listener, trusted []string) *proxyListener {
	l := &proxyListener{Listener: listener, trusted: make(map[string]bool), ready: make(chan net.Conn), done: make(chan struct{}), slots: make(chan struct{}, 64), pending: make(map[net.Conn]struct{}), headerTimeout: 2 * time.Second}
	for _, ip := range trusted {
		l.trusted[ip] = true
	}
	go l.serve()
	return l
}

func (l *proxyListener) serve() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			_ = l.Close()
			return
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok || !l.trusted[peer.IP.String()] {
			_ = conn.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = conn.Close()
			<-l.slots
			return
		}
		l.pending[conn] = struct{}{}
		l.mu.Unlock()
		go l.prepare(conn)
	}
}

func (l *proxyListener) prepare(conn net.Conn) {
	delivered := false
	defer func() {
		l.mu.Lock()
		delete(l.pending, conn)
		l.mu.Unlock()
		if !delivered {
			_ = conn.Close()
		}
		<-l.slots
	}()
	if err := conn.SetReadDeadline(time.Now().Add(l.headerTimeout)); err != nil {
		return
	}
	reader := bufio.NewReaderSize(conn, 256)
	header, err := proxyproto.Read(reader)
	if err != nil || header == nil || !header.Command.IsProxy() {
		return
	}
	source, ok := header.SourceAddr.(*net.TCPAddr)
	if !ok || source.IP == nil || source.IP.IsUnspecified() || source.IP.IsMulticast() || source.Port <= 0 {
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	wrapped := &proxyConn{Conn: conn, reader: reader, remote: source}
	select {
	case l.ready <- wrapped:
		delivered = true
	case <-l.done:
	}
}

func (l *proxyListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ready:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *proxyListener) Close() error {
	var err error
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		err = l.Listener.Close()
		for conn := range l.pending {
			_ = conn.Close()
		}
		l.mu.Unlock()
	})
	return err
}

type proxyConn struct {
	net.Conn
	reader *bufio.Reader
	remote net.Addr
}

func (c *proxyConn) RemoteAddr() net.Addr       { return c.remote }
func (c *proxyConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
