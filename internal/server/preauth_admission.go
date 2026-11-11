package server

import "net"

// The global cap must stay far above the per-source cap: at 256/32, eight
// sources could fill it and refuse every real login. 2048 pending handshakes
// (each holding TLS buffers) still fit the server's memory budget, and an
// attacker now needs 128 sources (IPv4 addresses or IPv6 /64s) to fill it.
const (
	maxPreauthConnections = 2048
	maxPreauthPerIP       = 16
	preauthFrameLimit     = 32 << 10
)

// releasePreauth is idempotent: authentication and connection cleanup both call it.
func (s *TCPServer) releasePreauth(conn net.Conn) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if ip, ok := s.preauth[conn]; ok {
		delete(s.preauth, conn)
		s.preauthByIP[ip]--
		if s.preauthByIP[ip] == 0 {
			delete(s.preauthByIP, ip)
		}
	}
}

func (s *TCPServer) authenticatedClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, c := range s.clients {
		if c.isAuthed() {
			count++
		}
	}
	return count
}
