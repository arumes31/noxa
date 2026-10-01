package server

import (
	"encoding/base64"

	"noxa/internal/netproto"
)

func canonicalDMKey(encoded string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

func (s *TCPServer) senderDMKey(client *Client) (string, bool) {
	if s.deps == nil || s.deps.State == nil {
		return "", false
	}
	current, ok := s.deps.State.GetClient(client.ID)
	if !ok || current.UniqueID != client.uniqueID() {
		return "", false
	}
	return canonicalDMKey(current.E2EPublicKey)
}

// boundDMRecipient selects only a live authenticated device that owns the key
// used for this ciphertext. It preserves the existing single-recipient policy.
// The second result distinguishes an offline account from a changed device key.
func (s *TCPServer) boundDMRecipient(uid, clientID, key string) (*Client, bool) {
	if s.deps == nil || s.deps.State == nil {
		return nil, false
	}
	s.mu.RLock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.RUnlock()
	online := false
	for _, client := range clients {
		if !client.isAuthed() || (uid != "" && client.uniqueID() != uid) || (clientID != "" && client.ID != clientID) {
			continue
		}
		online = true
		if published, ok := s.senderDMKey(client); ok && published == key {
			return client, true
		}
	}
	return nil, online
}

func (s *TCPServer) rejectStaleDMKey(client *Client) error {
	return s.writeMessage(client, netproto.MsgError, netproto.Error{
		Code: errCodeConflict, OriginType: uint16(netproto.MsgChatSend), RecipientKeyStale: true,
		Message: "recipient encryption key changed; refresh the key before retrying",
	})
}
