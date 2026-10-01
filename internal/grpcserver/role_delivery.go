package grpcserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/stats"
)

const roleDeliveryHeader = "noxa-read-delivery"

type roleDeliveryConnKey struct{}

// roleDeliveryTracker binds a gRPC connection context to the exact accepted
// socket. It observes completion at net.Conn.Write, after gRPC's output queues.
type roleDeliveryTracker struct {
	mu      sync.Mutex
	conns   map[string]*roleDeliveryConn
	workers sync.WaitGroup
}

func deliveryAddress(local, remote net.Addr) string {
	return local.Network() + ":" + local.String() + "/" + remote.Network() + ":" + remote.String()
}

func (t *roleDeliveryTracker) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	t.mu.Lock()
	conn := t.conns[deliveryAddress(info.LocalAddr, info.RemoteAddr)]
	t.mu.Unlock()
	if conn == nil {
		return ctx
	}
	return context.WithValue(ctx, roleDeliveryConnKey{}, conn)
}

func (*roleDeliveryTracker) HandleConn(context.Context, stats.ConnStats) {}
func (*roleDeliveryTracker) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}
func (*roleDeliveryTracker) HandleRPC(context.Context, stats.RPCStats) {}

type roleDeliveryListener struct {
	net.Listener
	tracker *roleDeliveryTracker
}

func (l *roleDeliveryListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	c := newRoleDeliveryConn(conn)
	c.tracker = l.tracker
	l.tracker.mu.Lock()
	if l.tracker.conns == nil {
		l.tracker.conns = make(map[string]*roleDeliveryConn)
	}
	l.tracker.conns[deliveryAddress(conn.LocalAddr(), conn.RemoteAddr())] = c
	l.tracker.mu.Unlock()
	return c, nil
}

type roleDeliveryFence struct {
	token string
	done  chan struct{}
	// Written before closing done; meaningful for message fences only.
	delivered bool
}

// writeMu protects the observer and joins in-flight writes after socket Close.
// pendingMu protects fences and stream mappings separately so a stalled write
// cannot block timeout registration or idle-stream cleanup.
type roleDeliveryConn struct {
	net.Conn
	tracker    *roleDeliveryTracker
	closed     atomic.Bool
	writeMu    sync.Mutex
	pendingMu  sync.Mutex
	pending    map[string]*roleDeliveryFence
	messages   map[string]*roleMessageStream
	streams    map[uint32]string
	buffer     []byte
	reader     bytes.Reader
	framer     *http2.Framer
	decoder    *hpack.Decoder
	headerID   uint32
	headerEnd  bool
	headerKey  string
	headerOpen bool
	dataFrame  *roleDataFrame
}

func newRoleDeliveryConn(conn net.Conn) *roleDeliveryConn {
	c := &roleDeliveryConn{Conn: conn, pending: make(map[string]*roleDeliveryFence), streams: make(map[uint32]string)}
	c.framer = http2.NewFramer(io.Discard, &c.reader)
	c.framer.SetMaxReadFrameSize(1 << 20)
	c.decoder = hpack.NewDecoder(4096, func(field hpack.HeaderField) {
		if field.Name == roleDeliveryHeader {
			c.headerKey = field.Value
		}
	})
	c.decoder.SetMaxStringLength(64 << 10)
	c.decoder.SetAllowedMaxDynamicTableSize(64 << 10)
	return c
}

func (c *roleDeliveryConn) register() (*roleDeliveryFence, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	f := &roleDeliveryFence{token: hex.EncodeToString(nonce[:]), done: make(chan struct{})}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	c.pending[f.token] = f
	return f, nil
}

func (c *roleDeliveryConn) finish(token string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if f := c.pending[token]; f != nil {
		delete(c.pending, token)
		close(f.done)
	}
}

func (c *roleDeliveryConn) expire(token string) {
	c.pendingMu.Lock()
	if c.pending[token] == nil {
		c.pendingMu.Unlock()
		return
	}
	// Claim closure before a concurrent successful completion can remove the
	// token. A timer which lost to completion must not close a healthy socket.
	c.closed.Store(true)
	c.pendingMu.Unlock()
	_ = c.Close()
}

func (c *roleDeliveryConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	if c.closed.Load() {
		c.writeMu.Unlock()
		return 0, net.ErrClosed
	}
	n, err := c.Conn.Write(p)
	if observeErr := c.observe(p[:n]); observeErr != nil && err == nil {
		err = observeErr
	}
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	c.writeMu.Unlock()
	if err != nil {
		_ = c.Close()
	}
	return n, err
}

func (c *roleDeliveryConn) Close() error {
	c.closed.Store(true)
	err := c.Conn.Close() // interrupt any blocked Read/Write before joining it
	c.writeMu.Lock()
	c.pendingMu.Lock()
	for key, fence := range c.pending {
		delete(c.pending, key)
		close(fence.done)
	}
	for key, stream := range c.messages {
		stream.closed = true
		delete(c.messages, key)
	}
	clear(c.streams)
	c.buffer, c.dataFrame = nil, nil
	c.reader.Reset(nil)
	c.pendingMu.Unlock()
	c.writeMu.Unlock()
	if c.tracker != nil {
		key := deliveryAddress(c.LocalAddr(), c.RemoteAddr())
		c.tracker.mu.Lock()
		if c.tracker.conns[key] == c {
			delete(c.tracker.conns, key)
		}
		c.tracker.mu.Unlock()
	}
	return err
}

// observe consumes only bytes accepted by the socket, never gRPC's buffered
// writes. All response headers are decoded to maintain HPACK table state; only
// our opaque marker is retained. Protected payload bytes are not retained.
func (c *roleDeliveryConn) observe(p []byte) error {
	for len(p) > 0 {
		if c.dataFrame != nil {
			n, err := c.observeDataFrame(p)
			if err != nil {
				return err
			}
			p = p[n:]
			continue
		}
		if len(c.buffer) < 9 {
			n := min(9-len(c.buffer), len(p))
			c.buffer = append(c.buffer, p[:n]...)
			p = p[n:]
			if len(c.buffer) < 9 {
				break
			}
		}
		length := int(c.buffer[0])<<16 | int(c.buffer[1])<<8 | int(c.buffer[2])
		if length > 1<<20 {
			return errors.New("grpc delivery frame exceeds limit")
		}
		if http2.FrameType(c.buffer[3]) == http2.FrameData {
			if err := c.beginDataFrame(length); err != nil {
				return err
			}
			c.buffer = nil
			continue
		}
		if len(c.buffer) < 9+length {
			n := min(9+length-len(c.buffer), len(p))
			c.buffer = append(c.buffer, p[:n]...)
			p = p[n:]
			if len(c.buffer) < 9+length {
				break
			}
		}
		c.reader.Reset(c.buffer)
		frame, err := c.framer.ReadFrame()
		c.reader.Reset(nil)
		if err != nil {
			return err
		}
		switch f := frame.(type) {
		case *http2.HeadersFrame:
			c.headerID, c.headerEnd, c.headerKey = f.StreamID, f.StreamEnded(), ""
			c.headerOpen = !f.HeadersEnded()
			err = c.headerFragment(f.HeaderBlockFragment(), f.HeadersEnded())
		case *http2.ContinuationFrame:
			c.headerOpen = !f.HeadersEnded()
			err = c.headerFragment(f.HeaderBlockFragment(), f.HeadersEnded())
		case *http2.RSTStreamFrame:
			c.finishStream(f.StreamID)
		}
		if err != nil {
			return err
		}
		c.buffer = nil
	}
	return nil
}

func (c *roleDeliveryConn) headerFragment(fragment []byte, end bool) error {
	if _, err := c.decoder.Write(fragment); err != nil {
		return err
	}
	if !end {
		return nil
	}
	if err := c.decoder.Close(); err != nil {
		return err
	}
	if c.headerKey != "" {
		c.pendingMu.Lock()
		if c.pending[c.headerKey] != nil || c.messages[c.headerKey] != nil {
			c.streams[c.headerID] = c.headerKey
		}
		c.pendingMu.Unlock()
	}
	if c.headerEnd {
		c.finishStream(c.headerID)
	}
	return nil
}

func (c *roleDeliveryConn) finishStream(id uint32) {
	c.pendingMu.Lock()
	token := c.streams[id]
	delete(c.streams, id)
	c.pendingMu.Unlock()
	if token != "" {
		c.finish(token)
		c.endMessageStream(token)
	}
}
