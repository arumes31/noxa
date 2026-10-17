// Typing notifications and direct-message delivery/read receipts.
package main

import (
	"noxa/internal/netproto"
)

// hideChatActivity reports the privacy setting that stops read receipts and
// typing indicators (DMs and private groups). It is enforced here, in the
// backend, so no frontend path can send either signal while it is on.
func (a *App) hideChatActivity() bool {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.settings.HideChatActivity
}

// SendTyping relays a typing indicator (channel scope or DM).
func (a *App) SendTyping(channelID int64, toUniqueID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	if a.hideChatActivity() {
		return ""
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

// SendTypingForTab sends activity only through the displayed server.
func (a *App) SendTypingForTab(tabID string, channelID int64, toUniqueID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	if a.hideChatActivity() {
		return ""
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

// SendConversationTypingForTab announces typing in a private group. It is a
// no-op (not an error) while the privacy setting is on and on servers without
// CapabilityConversationActivity, which would relay the frame as a global
// indicator.
func (a *App) SendConversationTypingForTab(tabID, groupID string) string {
	if !canonicalConversationID.MatchString(groupID) {
		return "invalid private group"
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	m.mu.Lock()
	supported := m.supportsConversationActivity
	m.mu.Unlock()
	if !supported || a.hideChatActivity() {
		return ""
	}
	if err := m.write(netproto.MsgTyping, netproto.Typing{ConversationID: groupID}); err != nil {
		return err.Error()
	}
	return ""
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

// SendChatRead acks a read DM (read receipt to the sender). With the privacy
// setting on it reports success without sending anything.
func (a *App) SendChatRead(toUniqueID, clientMsgID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	if a.hideChatActivity() {
		return ""
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

// SendChatReadForTab binds a read receipt to its source server.
func (a *App) SendChatReadForTab(tabID, toUniqueID, clientMsgID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	if a.hideChatActivity() {
		return ""
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

// shareConversationReads decides whether a private-group mark_read also
// shares the read receipt. The frontend's own value is never trusted.
func (a *App) shareConversationReads(m *connManager) bool {
	m.mu.Lock()
	supported := m.supportsConversationActivity
	m.mu.Unlock()
	return supported && !a.hideChatActivity()
}

func sendChatReadWith(m *connManager, toUniqueID, clientMsgID string) string {
	if err := m.write(netproto.MsgChatRead, netproto.ChatRead{
		ToUniqueID: toUniqueID, ClientMsgID: clientMsgID,
	}); err != nil {
		return err.Error()
	}
	return ""
}
