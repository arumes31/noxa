package turn

import (
	"net"
	"net/netip"
	"noxa/internal/traffic"
	"testing"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

func TestLocalRelayPairDoesNotRequireNATHairpin(t *testing.T) {
	m := NewManager("pair", 8, 1<<20)
	defer m.Close()
	listener, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The advertised documentation address cannot be routed to these sockets.
	// Both directions must use the registry's local-address translation.
	public := netip.MustParseAddr("203.0.113.77")
	counter := &traffic.Counter{}
	generator := &Generator{Traffic: counter, Manager: m, Base: &pion.RelayAddressGeneratorPortRange{RelayAddress: net.IP(public.AsSlice()), Address: "0.0.0.0", MinPort: 30000, MaxPort: 39999}, AllowedPeer: DestinationPolicy(nil, false, public)}
	logger := logging.NewDefaultLoggerFactory()
	logger.DefaultLogLevel = logging.LogLevelDisabled
	srv, err := pion.NewServer(pion.ServerConfig{Realm: "pair", AuthHandler: m.Authenticate, LoggerFactory: logger, PacketConnConfigs: []pion.PacketConnConfig{{PacketConn: listener, RelayAddressGenerator: generator}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	allocate := func(id string) (*pion.Client, net.PacketConn) {
		t.Helper()
		m.Register(id, id)
		m.Authorize(id, true)
		lease, e := m.Issue(id)
		if e != nil {
			t.Fatal(e)
		}
		conn, e := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = conn.Close() })
		c, e := pion.NewClient(&pion.ClientConfig{Conn: conn, STUNServerAddr: listener.LocalAddr().String(), TURNServerAddr: listener.LocalAddr().String(), Username: lease.Username, Password: lease.Password, Realm: "pair", LoggerFactory: logger})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(c.Close)
		if e = c.Listen(); e != nil {
			t.Fatal(e)
		}
		relay, e := c.Allocate()
		if e != nil {
			t.Fatal(e)
		}
		return c, relay
	}
	a, ra := allocate("a")
	b, rb := allocate("b")
	if err := a.CreatePermission(rb.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if err := b.CreatePermission(ra.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]net.PacketConn{{ra, rb}, {rb, ra}} {
		if _, err := pair[0].WriteTo([]byte("local relay packet"), pair[1].LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = pair[1].SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 64)
		n, from, err := pair[1].ReadFrom(buf)
		if err != nil || string(buf[:n]) != "local relay packet" || from.String() != pair[0].LocalAddr().String() {
			t.Fatalf("mapped relay: n=%d from=%v err=%v", n, from, err)
		}
	}
	if counts := counter.Snapshot(); counts.In != 2*int64(len("local relay packet")) || counts.Out != counts.In {
		t.Fatalf("relay socket traffic: %+v", counts)
	}
	m.Revoke("b")
	_, _ = ra.WriteTo([]byte("revoked relay packet"), rb.LocalAddr())
	_ = rb.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 64)
	if _, _, err := rb.ReadFrom(buf); err == nil {
		t.Fatal("local route bypassed revocation")
	}
}
