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
		return encryptFailure(encryptErr)
	}
	retry.ClientMsgID = msg.ClientMsgID
	return m.sendChatAcknowledgedOn(conn, retry)
}

// encryptFailure prefixes an encryption error for the send-error banner. A
// peer without an end-to-end key already reads as a complete sentence.
func encryptFailure(err error) error {
	if errors.Is(err, errPeerNoE2EKey) {
		return err
	}
	return fmt.Errorf("encryption failed: %w", err)
}
