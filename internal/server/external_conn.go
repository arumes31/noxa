// external_conn.go admits control connections accepted by another listener,
// such as WebSocket sessions from the mobile web app.
package server

import (
	"context"
	"errors"
	"net"
)

// ErrConnectionRejected reports that the server is not running or the
// pre-authentication admission limits are full.
var ErrConnectionRejected = errors.New("control connection rejected")

// ServeExternalConn runs the control protocol on conn exactly like a TCP
// connection: the same pre-auth limits, auth deadline, dispatcher and
// shutdown tracking apply. conn.RemoteAddr must report the client address.
// It blocks until the session ends and always closes conn.
func (s *TCPServer) ServeExternalConn(ctx context.Context, conn net.Conn) error {
	s.lifecycleMu.Lock()
	if s.lifecycleState != tcpLifecycleRunning {
		s.lifecycleMu.Unlock()
		_ = conn.Close()
		return ErrConnectionRejected
	}
	ip := remoteIP(conn)
	if len(s.preauth) >= maxPreauthConnections || s.preauthByIP[ip] >= maxPreauthPerIP {
		s.lifecycleMu.Unlock()
		_ = conn.Close()
		return ErrConnectionRejected
	}
	s.preauth[conn] = ip
	s.preauthByIP[ip]++
	s.acceptedConns[conn] = struct{}{}
	s.connWG.Add(1)
	s.lifecycleMu.Unlock()
	s.metricsSink().IncTCPConnections()
	s.serveConn(ctx, conn)
	return nil
}
