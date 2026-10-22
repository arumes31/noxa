package traffic

import (
	"io"
	"net"
	"testing"
)

type partialConn struct{ net.Conn }

func (partialConn) Read(p []byte) (int, error) { copy(p, "abc"); return 3, io.EOF }
func (partialConn) Write([]byte) (int, error)  { return 2, io.ErrShortWrite }
func TestPartialIOIsCountedEvenOnError(t *testing.T) {
	c := &Counter{}
	conn := WrapConn(partialConn{}, c)
	if n, err := conn.Read(make([]byte, 10)); n != 3 || err != io.EOF {
		t.Fatal(n, err)
	}
	if n, err := conn.Write([]byte("abcde")); n != 2 || err != io.ErrShortWrite {
		t.Fatal(n, err)
	}
	c.In(-1)
	c.Out(0)
	if c.Snapshot() != (Counts{3, 2}) {
		t.Fatal(c.Snapshot())
	}
	var absent *Counter
	absent.In(3)
	absent.Out(4)
	if absent.Snapshot() != (Counts{}) {
		t.Fatal("nil counter")
	}
}
func TestPacketSocketTraffic(t *testing.T) {
	a, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	counter := &Counter{}
	wrapped := WrapPacketConn(a, counter)
	if _, err := wrapped.WriteTo([]byte("abc"), b.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo([]byte("reply"), a.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wrapped.ReadFrom(make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if counter.Snapshot() != (Counts{5, 3}) {
		t.Fatal(counter.Snapshot())
	}
}
