package server

import (
	"slices"
	"time"

	"noxa/internal/netproto"
)

func (s *TCPServer) requiredAuthorizationModel() string {
	if s.roleBackend() != nil {
		return netproto.AuthorizationModelRolesV1
	}
	return ""
}

func (c *Client) beginAuthorizationNegotiation(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authorizationModel = model
	// A new attempt cannot reuse a challenge issued under an earlier negotiation.
	c.challenge = nil
	c.challengeUID = ""
	c.challengeNick = ""
	c.challengeExp = time.Time{}
}

func (c *Client) negotiatedAuthorizationModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authorizationModel
}

func (s *TCPServer) negotiateAuthorization(client *Client, models []string) bool {
	client.beginAuthorizationNegotiation("")
	required := s.requiredAuthorizationModel()
	if required == "" {
		return true
	}
	if len(models) > 8 || !slices.Contains(models, required) {
		return false
	}
	client.beginAuthorizationNegotiation(required)
	return true
}

func (s *TCPServer) rejectAuthorizationModel(client *Client) error {
	return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{
		AuthorizationModel: s.requiredAuthorizationModel(),
		Reason:             "unsupported authorization model; upgrade your noXa client",
	})
}
