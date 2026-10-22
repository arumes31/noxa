package turn

import (
	"net"
	"testing"
	"time"
)

func TestTCPListenerClosesAcceptedConnections(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := limitedTCPListener(listener, 2)
	client, err := (&net.Dialer{}).DialContext(t.Context(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	accepted, err := limited.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := limited.Close(); err != nil {
		t.Fatal(err)
	}
	_ = accepted.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := accepted.Read(b[:]); err == nil {
		t.Fatal("accepted socket survived listener shutdown")
	}
}
