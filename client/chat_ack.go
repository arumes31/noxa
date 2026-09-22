package main

import (
	"fmt"
	"time"

	"noxa/internal/netproto"
)

func (m *connManager) writeChatMutation(kind netproto.MessageType, messageID int64, msg any, acknowledge bool) error {
	if !acknowledge {
		return m.write(kind, msg)
	}
	f, err := m.request(kind, netproto.MsgChatMutationSaved, msg, 15*time.Second)
	if err != nil {
		return err
	}
	var saved netproto.ChatMutationSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err
	}
	if saved.Operation != kind || saved.MessageID != messageID {
		return fmt.Errorf("chat acknowledgement does not match the request; refresh before retrying")
	}
	return nil
}

func (m *connManager) sendChatAcknowledged(msg netproto.ChatSend) error {
	msg.AckRequested = m.usesRoleAuthorization()
	if !msg.AckRequested {
		return m.write(netproto.MsgChatSend, msg)
	}
	f, err := m.request(netproto.MsgChatSend, netproto.MsgChatAccepted, msg, 15*time.Second)
	if err != nil {
		return err
	}
	var accepted netproto.ChatAccepted
	if err := netproto.Decode(f, &accepted); err != nil {
		return err
	}
	if !accepted.Matches(msg) {
		return fmt.Errorf("send acknowledgement does not match the message; check history before retrying")
	}
	return nil
}
