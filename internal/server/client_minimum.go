package server

import (
	"noxa/internal/netproto"
	"noxa/internal/version"
)

func (s *TCPServer) clientMinimumPolicy() (version.ClientMinimum, error) {
	s.configMu.RLock()
	minimum := s.cfg.ClientMinVersion
	s.configMu.RUnlock()
	// In-process embedders/tests may use a zero-value Config. The production
	// loader supplies 0.0.0 and rejects explicit invalid/empty configuration.
	if minimum == "" {
		minimum = "0.0.0"
	}
	return version.NewClientMinimum(minimum, version.Short())
}

func (s *TCPServer) clientVersionRejection(clientVersion string) string {
	policy, err := s.clientMinimumPolicy()
	if err != nil {
		return "client version policy unavailable; contact the server administrator"
	}
	return policy.Rejection(clientVersion)
}

// announceClientVersions adds the client version policy to an AUTH response:
// the hard minimum and the version clients should update to in the background.
func (s *TCPServer) announceClientVersions(resp *netproto.AuthResponse) {
	if policy, err := s.clientMinimumPolicy(); err == nil {
		resp.ClientMinVersion = policy.Required()
	}
	s.configMu.RLock()
	configured := s.cfg.ClientRecommendedVersion
	s.configMu.RUnlock()
	resp.ClientRecommendedVersion, _ = version.ClientRecommended(configured, version.Short())
}

func (s *TCPServer) rejectClientVersion(client *Client, reason string) error {
	// Invalidate any earlier challenge before returning a rejection: a later
	// signature must not reuse a previously acceptable handshake.
	client.beginAuthorizationNegotiation("")
	resp := netproto.AuthResponse{AuthorizationModel: s.requiredAuthorizationModel(), Reason: reason}
	s.announceClientVersions(&resp)
	return s.writeMessage(client, netproto.MsgAuthResponse, resp)
}
