package main

import (
	"errors"
	"fmt"
	"net"

	"noxa/internal/netproto"
)

// sendChatWithKeyRetry retries only an explicit pre-delivery rejection. Socket
// errors, timeouts and uncertain acceptance must never cause a duplicate send.
func (m *connManager) sendChatWithKeyRetry(msg netproto.ChatSend, plaintext string) error {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	return m.sendChatWithKeyRetryOn(conn, msg, plaintext)
}

// The outbox captures its authenticated connection before encryption, too.
func (m *connManager) sendChatWithKeyRetryOn(conn net.Conn, msg netproto.ChatSend, plaintext string) error {
	err := m.sendChatAcknowledgedOn(conn, msg)
	var failure *requestFailure
	if msg.ToUniqueID != "" && errors.As(err, &failure) && failure.response.RecipientDevicesStale &&
		failure.response.OriginType == uint16(netproto.MsgChatSend) && m.dmDevicesActive() {
		return m.retryStaleDevices(conn, msg, plaintext, err)
	}
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

// retryStaleDevices handles RecipientDevicesStale: the server's device list
// differs from the copies (a device was linked or revoked). Both lists are
// refreshed and the message is sealed and sent once more under the same
// ClientMsgID, so the recipient can never see it twice.
func (m *connManager) retryStaleDevices(conn net.Conn, msg netproto.ChatSend, plaintext string, err error) error {
	m.mu.Lock()
	current := conn != nil && m.conn == conn
	ownUID := m.uniqueID
	m.mu.Unlock()
	if !current {
		return err
	}
	m.forgetDMDevices(msg.ToUniqueID, ownUID)
	retry, encryptErr := m.encryptChat("direct", msg.ToUniqueID, plaintext)
	if encryptErr != nil {
		return encryptFailure(encryptErr)
	}
	retry.ClientMsgID = msg.ClientMsgID
	err = m.sendChatAcknowledgedOn(conn, retry)
	var failure *requestFailure
	if len(retry.Copies) == 0 && errors.As(err, &failure) && failure.response.RecipientDevicesStale && !m.selfDeviceTrusted() {
		// The single copy was refused because the contact has several devices,
		// and this desktop cannot send per-device copies until it is confirmed.
		return errDeviceUnconfirmed
	}
	return err
}

// errDeviceUnconfirmed explains why a direct message to a contact with
// several devices cannot be sent from an unconfirmed desktop.
var errDeviceUnconfirmed = errors.New("this desktop is not confirmed for direct messages yet; confirm it under \"Other login for this server\" on your first noXa device")

// selfDeviceTrusted reports whether this desktop is a verified device of its
// own account, i.e. may send per-device copies.
func (m *connManager) selfDeviceTrusted() bool {
	id, err := m.identity()
	if err != nil {
		return false
	}
	pub, _, err := id.x25519()
	if err != nil {
		return false
	}
	m.mu.Lock()
	ownUID, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	devices, err := m.dmDeviceList(ownUID, false)
	if err != nil {
		return false
	}
	for _, d := range devices {
		if d.DeviceID == selfDevice && d.X25519 == pub {
			return true
		}
	}
	return false
}

// encryptFailure prefixes an encryption error for the send-error banner. A
// peer without an end-to-end key already reads as a complete sentence.
func encryptFailure(err error) error {
	if errors.Is(err, errPeerNoE2EKey) {
		return err
	}
	return fmt.Errorf("encryption failed: %w", err)
}
