package webrtc

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v5"
)

const mediaSocketWriteTimeout = 250 * time.Millisecond

// mediaSendBufferBytes gives the shared socket headroom for paced video bursts
// from many peers so a full kernel send queue does not block voice writes.
const mediaSendBufferBytes = 4 * 1024 * 1024

// configureMediaSendBuffer asks the kernel for send headroom on the shared UDP
// socket. The operating system may clamp this; no host setting is changed.
func configureMediaSendBuffer(conn net.PacketConn) error {
	buffer, ok := conn.(interface{ SetWriteBuffer(int) error })
	if !ok {
		return nil
	}
	return buffer.SetWriteBuffer(mediaSendBufferBytes)
}

// mediaOpusPayloadType is the payload type registered for Opus in the media
// engine. SRTP leaves the RTP header in clear, so the socket layer can tell
// voice from video without decrypting anything.
const mediaOpusPayloadType = 111

// isVoiceRTP reports whether an outgoing datagram is an (S)RTP Opus packet.
// STUN (0-3), DTLS (20-63) and RTCP (PT 200-206, i.e. 72-78 masked) never match.
func isVoiceRTP(b []byte) bool {
	return len(b) >= 12 && b[0]>>6 == 2 && b[1]&0x7f == mediaOpusPayloadType
}

// socketWriteGate serializes deadline ownership on shared ICE sockets. Waiting
// for another writer consumes the same deadline as the eventual network write.
// Deadline updates can shorten an active write but cannot extend its budget.
//
// Ownership is handed over in strict priority order: a voice writer waiting
// for the socket always goes before every waiting video/control writer, so a
// burst of video writes from many peers on the shared socket cannot delay
// voice for other peers beyond the single write currently in progress.
type socketWriteGate struct {
	qmu         sync.Mutex
	busy        bool
	high, low   []chan struct{} // FIFO waiters per class; buffered(1) grants
	mu          sync.Mutex
	active      time.Time
	requested   time.Time
	acquireHook func() // test-only: runs after a writer queued itself
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

func (g *socketWriteGate) acquire(deadline time.Time, voice bool) bool {
	g.qmu.Lock()
	if !g.busy {
		g.busy = true
		g.qmu.Unlock()
		return true // Uncontended fast path: no timer, no allocation.
	}
	grant := make(chan struct{}, 1)
	if voice {
		g.high = append(g.high, grant)
	} else {
		g.low = append(g.low, grant)
	}
	hook := g.acquireHook
	g.qmu.Unlock()
	if hook != nil {
		hook()
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-grant:
		return true
	case <-timer.C:
	}
	g.qmu.Lock()
	if removeWaiter(&g.high, grant) || removeWaiter(&g.low, grant) {
		g.qmu.Unlock()
		return false
	}
	g.qmu.Unlock()
	// Ownership was granted concurrently with the timeout; pass it on.
	<-grant
	g.release()
	return false
}

func removeWaiter(queue *[]chan struct{}, grant chan struct{}) bool {
	for i, waiter := range *queue {
		if waiter == grant {
			*queue = append((*queue)[:i], (*queue)[i+1:]...)
			return true
		}
	}
	return false
}

func (g *socketWriteGate) release() {
	g.qmu.Lock()
	defer g.qmu.Unlock()
	for _, queue := range []*[]chan struct{}{&g.high, &g.low} {
		if len(*queue) > 0 {
			next := (*queue)[0]
			(*queue)[0] = nil
			*queue = (*queue)[1:]
			next <- struct{}{}
			return
		}
	}
	g.busy = false
}

func (g *socketWriteGate) write(set func(time.Time) error, write func() (int, error)) (int, error) {
	return g.writePriority(false, set, write)
}

func (g *socketWriteGate) writePriority(voice bool, set func(time.Time) error, write func() (int, error)) (int, error) {
	deadline := time.Now().Add(mediaSocketWriteTimeout)
	if !g.acquire(deadline, voice) {
		return 0, os.ErrDeadlineExceeded
	}
	defer g.release()
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
	dscp   voiceDSCP
}

func (c *boundedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	voice := isVoiceRTP(b)
	return c.writes.writePriority(voice, c.PacketConn.SetWriteDeadline, func() (int, error) {
		if voice {
			if n, ok, err := writeVoiceMarked(&c.dscp, c.PacketConn, b, addr); ok {
				return n, err
			}
		}
		return c.PacketConn.WriteTo(b, addr)
	})
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
	return c.writes.writePriority(isVoiceRTP(b), c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.Write(b) })
}
func (c *boundedUDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.writes.writePriority(isVoiceRTP(b), c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.WriteTo(b, addr) })
}
func (c *boundedUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	return c.writes.writePriority(isVoiceRTP(b), c.UDPConn.SetWriteDeadline, func() (int, error) { return c.UDPConn.WriteToUDP(b, addr) })
}
func (c *boundedUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	n, err = c.writes.writePriority(isVoiceRTP(b), c.UDPConn.SetWriteDeadline, func() (int, error) { var e error; n, oobn, e = c.UDPConn.WriteMsgUDP(b, oob, addr); return n, e })
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
	if buffer, ok := c.(interface{ SetWriteBuffer(int) error }); ok {
		_ = buffer.SetWriteBuffer(mediaSendBufferBytes) // Best effort, as for the shared socket.
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
