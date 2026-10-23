// A local, ephemeral TURN relay for Playwright private-call tests.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"

	"github.com/pion/logging"
	turn "github.com/pion/turn/v5"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}
func run() error {
	host := "127.0.0.1"
	// Chromium on Windows does not gather UDP on the loopback adapter.
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, addr := range addresses {
		if ip, ok := addr.(*net.IPNet); ok && ip.IP.To4() != nil && ip.IP.IsGlobalUnicast() && !ip.IP.IsLoopback() {
			host = ip.IP.String()
			break
		}
	}
	listener, err := net.ListenPacket("udp4", net.JoinHostPort(host, "0"))
	if err != nil {
		return err
	}
	defer listener.Close()
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	credential := hex.EncodeToString(secret)
	logger := logging.NewDefaultLoggerFactory()
	logger.DefaultLogLevel = logging.LogLevelDisabled
	server, err := turn.NewServer(turn.ServerConfig{
		Realm: "playwright", LoggerFactory: logger,
		AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
			if a.Username != "fixture" || a.Realm != "playwright" {
				return "", nil, false
			}
			return a.Username, turn.GenerateAuthKey(a.Username, a.Realm, credential), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: listener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP(host), Address: host},
			PermissionHandler:     func(_ net.Addr, peer net.IP) bool { return peer.Equal(net.ParseIP(host)) },
		}},
	})
	if err != nil {
		return err
	}
	defer server.Close()
	if err = json.NewEncoder(os.Stdout).Encode([]map[string]string{{"urls": "turn:" + listener.LocalAddr().String() + "?transport=udp", "username": "fixture", "credential": credential}}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}
