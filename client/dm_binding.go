package main

import (
	"errors"
	"fmt"

	"noxa/internal/netproto"
)

// sendChatWithKeyRetry retries only an explicit pre-delivery rejection. Socket
// errors, timeouts and uncertain acceptance must never cause a duplicate send.
func (m *connManager) sendChatWithKeyRetry(msg netproto.ChatSend, plaintext string) error {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	err := m.sendChatAcknowledgedOn(conn, msg)
	var failure *requestFailure
	if msg.ToUniqueID == "" || msg.RecipientPublicKey == "" || !errors.As(err, &failure) ||
		!failure.response.RecipientKeyStale || failure.response.OriginType != uint16(netproto.MsgChatSend) {
		return err
	}
	m.mu.Lock()
	current := conn != nil && m.conn == conn
	m.mu.Unlock()
	if !current {
		return err
	}
	rejected, valid := parseDMPublicKey(msg.RecipientPublicKey)
	if !valid {
		return err
	}
	m.pubKeys.forgetKey(msg.ToUniqueID, rejected)
	retry, encryptErr := m.encryptChat("direct", msg.ToUniqueID, plaintext)
	if encryptErr != nil {
		return fmt.Errorf("encryption failed: %w", encryptErr)
	}
	retry.ClientMsgID = msg.ClientMsgID
	return m.sendChatAcknowledgedOn(conn, retry)
}
