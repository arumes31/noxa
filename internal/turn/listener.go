package turn

import (
	"context"
	"errors"

	"net"
	"net/netip"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
	"noxa/internal/config"
	"noxa/internal/traffic"
)

// DestinationPolicy checks exact private endpoints. Optional public peers are
// needed by the existing private-call mesh; they never admit special networks.
func DestinationPolicy(exact []string, public bool, self ...netip.Addr) func(net.Addr) bool {
	allowed := make(map[netip.AddrPort]bool)
	for _, value := range exact {
		if ap, err := netip.ParseAddrPort(value); err == nil {
			allowed[ap] = true
		}
	}
	denied := []netip.Prefix{}
	for _, value := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"} {
		denied = append(denied, netip.MustParsePrefix(value))
	}
	return func(address net.Addr) bool {
		udp, ok := address.(*net.UDPAddr)
		if !ok || udp.Port <= 0 {
			return false
		}
		ap := udp.AddrPort()
		ip := ap.Addr().Unmap()
		ap = netip.AddrPortFrom(ip, ap.Port())
		if allowed[ap] {
			return true
		}
		for _, own := range self {
			if ip == own.Unmap() {
				return false
			}
		}
		if !public || !ip.Is4() {
			return false
		}
		for _, prefix := range denied {
			if prefix.Contains(ip) {
				return false
			}
		}
		return true
	}
}

// Listen serves TURN over UDP and TCP with UDP relay sockets. Listeners are
// never opened on a partially validated configuration and share one registry.
func Listen(ctx context.Context, cfg config.TURNConfig, m *Manager, counter *traffic.Counter) (func() error, error) {
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = "0.0.0.0"
	}
	generator := &Generator{Traffic: counter, Manager: m, Base: &pion.RelayAddressGeneratorPortRange{RelayAddress: net.ParseIP(cfg.PublicIP), Address: host, MinPort: cfg.RelayMinPort, MaxPort: cfg.RelayMaxPort}, AllowedPeer: DestinationPolicy(cfg.AllowedPeers, cfg.AllowPublicPeers, netip.MustParseAddr(cfg.PublicIP))}
	if err := generator.Validate(); err != nil {
		return nil, err
	}
	udp, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp4", cfg.ListenAddr)
	if err != nil {
		return nil, err
	}
	tcp, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", cfg.ListenAddr)
	if err != nil {
		_ = udp.Close()
		return nil, err
	}
	logger := logging.NewDefaultLoggerFactory()
	logger.DefaultLogLevel = logging.LogLevelDisabled
	limited := limitedTCPListener(traffic.WrapListener(tcp, counter), cfg.MaxAllocations*2+64)
	relay, err := pion.NewServer(pion.ServerConfig{Realm: cfg.Realm, AuthHandler: m.Authenticate, EventHandler: limited.events(), LoggerFactory: logger,
		PacketConnConfigs: []pion.PacketConnConfig{{PacketConn: traffic.WrapPacketConn(udp, counter), RelayAddressGenerator: generator}},
		ListenerConfigs:   []pion.ListenerConfig{{Listener: limited, RelayAddressGenerator: generator}},
	})
	if err != nil {
		_ = udp.Close()
		_ = tcp.Close()
		return nil, err
	}
	// Keep independent allocation events for each TCP listener; both transports
	// share the same credentials, session limits, revocation and relay generator.
	var proxyRelay *pion.Server
	if cfg.TLSProxyListenAddr != "" {
		backend, listenErr := (&net.ListenConfig{}).Listen(ctx, "tcp4", cfg.TLSProxyListenAddr)
		if listenErr != nil {
			_ = relay.Close()
			return nil, listenErr
		}
		proxy := newProxyListener(backend, cfg.TLSProxyTrustedIPs)
		proxied := limitedTCPListener(traffic.WrapListener(proxy, counter), cfg.MaxAllocations*2+64)
		proxyRelay, err = pion.NewServer(pion.ServerConfig{Realm: cfg.Realm, AuthHandler: m.Authenticate, EventHandler: proxied.events(), LoggerFactory: logger,
			ListenerConfigs: []pion.ListenerConfig{{Listener: proxied, RelayAddressGenerator: generator}},
		})
		if err != nil {
			_ = proxied.Close()
			_ = relay.Close()
			return nil, err
		}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				m.Sweep()
			}
		}
	}()
	return func() error {
		close(done)
		m.Close()
		err := relay.Close()
		if proxyRelay != nil {
			err = errors.Join(err, proxyRelay.Close())
		}
		return err
	}, nil
}
