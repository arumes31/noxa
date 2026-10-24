package main

import (
	"crypto/ed25519"
	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

// Historical chains authenticate persisted group messages only. They never
// populate dmDevices, which determines recipients and live-call trust.
func (m *connManager) trustedHistoricalConversationSender(message netproto.ConversationMessage, key [32]byte) bool {
	if len(message.SenderDeviceChain) == 0 || len(message.SenderDeviceChain) > netproto.MaxConversationSenderChain {
		return false
	}
	id, err := m.identity()
	if err != nil {
		return false
	}
	m.mu.Lock()
	ownUID, conn := m.uniqueID, m.conn
	m.mu.Unlock()
	var signing ed25519.PublicKey
	var encryption [32]byte
	if message.FromUniqueID == ownUID {
		signing, err = auth.LoadPublicKey(id.PublicKey)
		if err != nil {
			return false
		}
		encryption, _, err = id.x25519()
		if err != nil {
			return false
		}
	} else {
		response, err := m.requestDeviceDirectory(conn, message.FromUniqueID)
		if err != nil {
			return false
		}
		encryption, err = m.verifyPeerRecord(message.FromUniqueID, response.PublicKey, false)
		if err != nil {
			return false
		}
		record, err := e2ee.ParseKeyRecord(response.PublicKey, message.FromUniqueID)
		if err != nil {
			return false
		}
		signing = record.SigningKey
	}
	devices, err := verifyDMDevices(message.FromUniqueID, signing, encryption, message.SenderDeviceChain)
	if err != nil {
		return false
	}
	for _, d := range devices {
		if d.DeviceID == message.SenderDeviceID && d.X25519 == key {
			return true
		}
	}
	return false
}
