// Typing notifications and direct-message delivery/read receipts.
package main

import (
	"noxa/internal/netproto"
)

// SendTyping relays a typing indicator (channel scope or DM).
func (a *App) SendTyping(channelID int64, toUniqueID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

// SendTypingForTab sends activity only through the displayed server.
func (a *App) SendTypingForTab(tabID string, channelID int64, toUniqueID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

func sendTypingWith(m *connManager, channelID int64, toUniqueID string) string {
	if err := m.write(netproto.MsgTyping, netproto.Typing{
		ChannelID: channelID, ToUniqueID: toUniqueID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// SendChatDelivered acks a received DM (delivery receipt to the sender).
func (a *App) SendChatDelivered(toUniqueID, clientMsgID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendChatDeliveredWith(m, toUniqueID, clientMsgID)
}

// SendChatDeliveredForTab binds a delivery receipt to its source server.
func (a *App) SendChatDeliveredForTab(tabID, toUniqueID, clientMsgID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendChatDeliveredWith(m, toUniqueID, clientMsgID)
}

func sendChatDeliveredWith(m *connManager, toUniqueID, clientMsgID string) string {
	if err := m.write(netproto.MsgChatDelivered, netproto.ChatDelivered{
		ToUniqueID: toUniqueID, ClientMsgID: clientMsgID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// SendChatRead acks a read DM (read receipt to the sender).
func (a *App) SendChatRead(toUniqueID, clientMsgID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

// SendChatReadForTab binds a read receipt to its source server.
func (a *App) SendChatReadForTab(tabID, toUniqueID, clientMsgID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

func sendChatReadWith(m *connManager, toUniqueID, clientMsgID string) string {
	if err := m.write(netproto.MsgChatRead, netproto.ChatRead{
		ToUniqueID: toUniqueID, ClientMsgID: clientMsgID,
	}); err != nil {
		return err.Error()
	}
	return ""
}
