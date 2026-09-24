package server

import "context"

// ClientIsBot reports authenticated account metadata. Bot status affects only
// presentation; role assignments remain the sole source of authority.
func (s *TCPServer) ClientIsBot(_ context.Context, client *Client) bool {
	if client == nil || client.userID() == 0 {
		return false
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.bot
}
