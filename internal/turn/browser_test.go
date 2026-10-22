package turn

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

// Opt in after installing client/frontend's Playwright Chromium. This uses real
// browser ICE/SRTP/SCTP through real Pion TURN, shortening only fixture deadlines
// and the browser polling interval to exercise several generations in seconds.
func TestBrowserRelayContinuity(t *testing.T) {
	if os.Getenv("NOXA_TURN_BROWSER") != "1" {
		t.Skip("set NOXA_TURN_BROWSER=1 to run real Chromium continuity verification")
	}
	for _, transport := range []string{"udp", "tcp", "tls"} {
		t.Run(transport, func(t *testing.T) {
			testIP := "127.0.0.1"
			// Chromium does not gather UDP candidates on Windows' loopback adapter.
			// Use an active local IPv4 interface, without sending traffic off-host.
			if addresses, err := net.InterfaceAddrs(); err == nil {
				for _, address := range addresses {
					if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && network.IP.IsGlobalUnicast() && !network.IP.IsLoopback() {
						testIP = network.IP.String()
						break
					}
				}
			}
			if configured := os.Getenv("NOXA_TURN_TEST_IP"); configured != "" {
				testIP = configured
			}
			m := NewManager("browser", 64, 1<<24)
			defer m.Close()
			udp, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", net.JoinHostPort(testIP, "0"))
			if err != nil {
				t.Fatal(err)
			}
			tcp, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", net.JoinHostPort(testIP, "0"))
			if err != nil {
				t.Fatal(err)
			}
			logger := logging.NewDefaultLoggerFactory()
			logger.DefaultLogLevel = logging.LogLevelDisabled
			generator := &Generator{Manager: m, Base: &pion.RelayAddressGeneratorPortRange{RelayAddress: net.ParseIP(testIP), Address: testIP, MinPort: 40000, MaxPort: 49999}, AllowedPeer: func(a net.Addr) bool { u, ok := a.(*net.UDPAddr); return ok && u.IP.Equal(net.ParseIP(testIP)) }}
			stream := tcp
			tlsAddress, tlsPin := "", ""
			if transport == "tls" {
				stream = newProxyListener(tcp, []string{testIP})
				proxy, _, pin := testTLSProxy(t, tcp.Addr().String(), testIP)
				tlsAddress, tlsPin = proxy.Addr().String(), pin
			}
			limited := limitedTCPListener(stream, 64)
			srv, err := pion.NewServer(pion.ServerConfig{Realm: "browser", AuthHandler: m.Authenticate, EventHandler: limited.events(), LoggerFactory: logger, PacketConnConfigs: []pion.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: generator}}, ListenerConfigs: []pion.ListenerConfig{{Listener: limited, RelayAddressGenerator: generator}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.Close() }()
			address := udp.LocalAddr().String()
			if transport == "tcp" {
				address = tcp.Addr().String()
			}
			uri := "turn:" + address + "?transport=" + transport
			if transport == "tls" {
				uri = "turns:" + tlsAddress + "?transport=tcp"
			}
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			module, err := os.ReadFile(filepath.Join(root, "client/frontend/src/relay-renewal.js"))
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				m.Register(id, id)
				m.Authorize(id, true)
			}
			token, err := randomCredential()
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<!doctype html><title>Relay test</title>"))
			})
			mux.HandleFunc("/module.js", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/javascript")
				_, _ = w.Write(module)
			})
			mux.HandleFunc("/lease", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Token") != token {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				id := r.URL.Query().Get("id")
				if id != "a" && id != "b" {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				m.Authorize(id, true)
				lease, e := m.Issue(id)
				if e != nil {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				m.mu.Lock()
				g := m.grants[lease.Username]
				if g.lease.ExpiresAt-g.lease.IssuedAt > 10000 {
					g.expires = m.now().Add(8 * time.Second)
					g.lease.ExpiresAt = g.expires.UnixMilli()
					g.lease.RefreshAt = m.now().Add(4500 * time.Millisecond).UnixMilli()
				}
				lease = g.lease
				m.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				_ = json.NewEncoder(w).Encode(map[string]any{"generation": lease.Generation, "server_time": time.Now().UnixMilli(), "expires_at": lease.ExpiresAt, "refresh_at": lease.RefreshAt, "ice_servers": []map[string]any{{"urls": []string{uri}, "username": lease.Username, "credential": lease.Password}}})
			})
			httpServer := httptest.NewServer(mux)
			defer httpServer.Close()
			ctx := t.Context()
			go func() {
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						m.Sweep()
					}
				}
			}()
			command := exec.CommandContext(ctx, "node", filepath.Join(root, "scripts/test-turn-browser.mjs"), httpServer.URL)
			command.Env = append(os.Environ(), "NOXA_TEST_RELAY_TOKEN="+token, "NOXA_TEST_TLS_PIN="+tlsPin)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
