package server

import "net"

const (
	maxPreauthConnections = 256
	maxPreauthPerIP       = 32
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
