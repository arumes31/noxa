package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// A streaming RPC has one marker in its initial headers and at most one
// protected message in flight. DATA framing is observed under writeMu; state
// shared with Send and cancellation is protected by pendingMu.
type roleMessageStream struct {
	token     string
	fence     *roleDeliveryFence
	closed    bool
	prefix    [5]byte
	prefixLen int
	remaining uint32
}

// DATA frames bypass http2.Framer because it retains its last payload buffer.
// Keep only lengths and flags, even when a socket write ends mid-frame.
type roleDataFrame struct {
	id         uint32
	remaining  int
	padding    int
	readPadLen bool
	end        bool
}

func (c *roleDeliveryConn) beginDataFrame(length int) error {
	id := binary.BigEndian.Uint32(c.buffer[5:9]) & 0x7fffffff
	flags := http2.Flags(c.buffer[4])
	if id == 0 || c.headerOpen {
		return errors.New("invalid grpc DATA frame order or stream")
	}
	padded := flags.Has(http2.FlagDataPadded)
	if padded && length == 0 {
		return errors.New("invalid empty padded DATA frame")
	}
	c.dataFrame = &roleDataFrame{id: id, remaining: length, readPadLen: padded, end: flags.Has(http2.FlagDataEndStream)}
	if length == 0 {
		c.completeDataFrame()
	}
	return nil
}

func (c *roleDeliveryConn) completeDataFrame() {
	if c.dataFrame.end {
		c.finishStream(c.dataFrame.id)
	}
	c.dataFrame = nil
}

func (c *roleDeliveryConn) observeDataFrame(data []byte) (int, error) {
	f := c.dataFrame
	consumed := 0
	if f.readPadLen {
		f.padding, f.readPadLen = int(data[0]), false
		f.remaining--
		data, consumed = data[1:], 1
		if f.padding > f.remaining {
			return consumed, errors.New("invalid DATA padding length")
		}
	}
	n := min(len(data), f.remaining-f.padding)
	if err := c.observeMessageData(f.id, data[:n]); err != nil {
		return consumed, err
	}
	f.remaining -= n
	consumed += n
	data = data[n:]
	if f.remaining == f.padding {
		n = min(len(data), f.padding)
		f.remaining -= n
		f.padding -= n
		consumed += n
	}
	if f.remaining == 0 {
		c.completeDataFrame()
	}
	return consumed, nil
}

func (c *roleDeliveryConn) newMessageStream() (*roleMessageStream, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	s := &roleMessageStream{token: hex.EncodeToString(nonce[:])}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	if c.messages == nil {
		c.messages = make(map[string]*roleMessageStream)
	}
	c.messages[s.token] = s
	return s, nil
}

func (c *roleDeliveryConn) armMessage(s *roleMessageStream) (*roleDeliveryFence, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.closed.Load() || s.closed || c.messages[s.token] != s {
		return nil, net.ErrClosed
	}
	if s.fence != nil {
		return nil, errors.New("protected stream already has a pending message")
	}
	f := &roleDeliveryFence{token: hex.EncodeToString(nonce[:]), done: make(chan struct{})}
	s.fence, c.pending[f.token] = f, f
	return f, nil
}

// Called only after the underlying socket accepted this complete DATA frame.
// The five-byte gRPC prefix may itself span DATA frames. Only prefix/length
// state is retained; message bodies (including compressed bodies) are skipped.
func (c *roleDeliveryConn) observeMessageData(id uint32, data []byte) error {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	s := c.messages[c.streams[id]]
	if s == nil {
		return nil // unary or an unprotected stream
	}
	for len(data) > 0 {
		if s.closed || s.fence == nil {
			return errors.New("unfenced protected stream message")
		}
		if s.prefixLen < len(s.prefix) {
			n := copy(s.prefix[s.prefixLen:], data)
			s.prefixLen += n
			data = data[n:]
			if s.prefixLen < len(s.prefix) {
				continue
			}
			if s.prefix[0] > 1 {
				return errors.New("invalid grpc message compression flag")
			}
			s.remaining = binary.BigEndian.Uint32(s.prefix[1:])
			if s.remaining > maxProtectedGRPCResponseBytes {
				return errors.New("protected stream message exceeds limit")
			}
		}
		n := min(len(data), int(s.remaining))
		s.remaining -= uint32(n)
		data = data[n:]
		if s.remaining == 0 {
			f := s.fence
			s.fence, s.prefixLen = nil, 0
			delete(c.pending, f.token)
			f.delivered = true
			close(f.done)
		}
	}
	return nil
}

func (c *roleDeliveryConn) endMessageStream(token string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, streamToken := range c.streams {
		if streamToken == token {
			delete(c.streams, id)
		}
	}
	if s := c.messages[token]; s != nil {
		s.closed = true
		delete(c.messages, token)
		if s.fence != nil {
			delete(c.pending, s.fence.token)
			close(s.fence.done)
			s.fence = nil
		}
	}
}

type protectedStreamDelivery struct {
	conn  *roleDeliveryConn
	state *roleMessageStream
}

func newProtectedStreamDelivery(ctx context.Context, setHeader func(metadata.MD) error) (*protectedStreamDelivery, error) {
	c, ok := ctx.Value(roleDeliveryConnKey{}).(*roleDeliveryConn)
	if !ok || c.tracker == nil {
		return nil, status.Error(codes.FailedPrecondition, "protected stream transport required")
	}
	s, err := c.newMessageStream()
	if err != nil {
		return nil, err
	}
	if err := setHeader(metadata.Pairs(roleDeliveryHeader, s.token)); err != nil {
		c.endMessageStream(s.token)
		return nil, err
	}
	return &protectedStreamDelivery{conn: c, state: s}, nil
}

// send must run inside the backend's current-admission/current-policy callback.
// Returning from gRPC Send only queues data; this method retains that callback
// until the complete message reaches the socket, or socket closure joins all
// writes. Calls must be serialized. It does not authorize content itself.
func (d *protectedStreamDelivery) send(ctx context.Context, value proto.Message, send func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if proto.Size(value) > maxProtectedGRPCResponseBytes {
		return status.Error(codes.ResourceExhausted, "protected response exceeds limit")
	}
	f, err := d.conn.armMessage(d.state)
	if err != nil {
		return err
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	expired := make(chan struct{})
	stop := context.AfterFunc(deliveryCtx, func() {
		d.conn.expire(f.token)
		close(expired)
	})
	defer func() {
		// This also runs on panic: queued protected bytes must not outlive
		// the caller's policy callback when Send fails unexpectedly.
		d.conn.expire(f.token)
		if !stop() {
			<-expired
		}
	}()
	if err := send(); err != nil {
		// Send may have partially queued output before failing. Closing the
		// socket and joining writes is required before releasing the lease.
		d.conn.expire(f.token)
		return err
	}
	<-f.done
	if !f.delivered {
		if err := deliveryCtx.Err(); err != nil {
			return err
		}
		return net.ErrClosed
	}
	return nil
}

func (d *protectedStreamDelivery) close() {
	d.conn.pendingMu.Lock()
	d.state.closed = true
	f := d.state.fence
	d.conn.pendingMu.Unlock()
	if f != nil {
		d.conn.expire(f.token)
	}
	// Peer cancellation can end a gRPC handler without any outbound terminal
	// frame. Retire the socket observer's reverse mapping explicitly as well.
	d.conn.endMessageStream(d.state.token)
}
