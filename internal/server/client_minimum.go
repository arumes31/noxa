package server

import (
	"noxa/internal/netproto"
	"noxa/internal/version"
)

func (s *TCPServer) clientVersionRejection(clientVersion string) string {
	s.configMu.RLock()
	minimum := s.cfg.ClientMinVersion
	s.configMu.RUnlock()
	// In-process embedders/tests may use a zero-value Config. The production
	// loader supplies 0.0.0 and rejects explicit invalid/empty configuration.
	if minimum == "" {
		minimum = "0.0.0"
	}
	policy, err := version.NewClientMinimum(minimum, version.Short())
	if err != nil {
		return "client version policy unavailable; contact the server administrator"
	}
	return policy.Rejection(clientVersion)
}

func (s *TCPServer) rejectClientVersion(client *Client, reason string) error {
	// Invalidate any earlier challenge before returning a rejection: a later
	// signature must not reuse a previously acceptable handshake.
	client.beginAuthorizationNegotiation("")
	return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{
		AuthorizationModel: s.requiredAuthorizationModel(), Reason: reason,
	})
}
