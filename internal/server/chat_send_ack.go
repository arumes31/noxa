package server

import (
	"encoding/json"
	"strconv"

	"noxa/internal/netproto"
)

// Identifier bounds shared by chat sends, receipts and typing relays. They are
// copied into every broadcast and spool row, so an unbounded value would
// multiply far beyond the body cap.
const (
	maxClientMsgIDBytes = 128
	maxUniqueIDBytes    = 256
)

// boundedChatIDs reports whether every identifier a chat send carries fits
// its bound, whether or not an acknowledgement was requested.
func boundedChatIDs(msg netproto.ChatSend) bool {
	return len(msg.ClientMsgID) <= maxClientMsgIDBytes && len(msg.RefClientMsgID) <= maxClientMsgIDBytes &&
		len(msg.ToUniqueID) <= maxUniqueIDBytes && len(msg.ToClientID) <= maxUniqueIDBytes
}

func validAcknowledgedChat(msg netproto.ChatSend) bool {
	if msg.ClientMsgID == "" || len(msg.ClientMsgID) > maxClientMsgIDBytes {
		return false
	}
	if msg.ToUniqueID != "" || msg.ToClientID != "" {
		return msg.ChannelID == "" && msg.ReplyToID == 0 && (msg.ToUniqueID == "" || msg.ToClientID == "")
	}
	if msg.ChannelID == "" {
		return true
	}
	id, err := strconv.ParseInt(msg.ChannelID, 10, 64)
	return err == nil && id > 0
}

func (s *TCPServer) acknowledgeChat(client *Client, msg netproto.ChatSend, disposition string, messageID int64) error {
	if !msg.AckRequested {
		return nil
	}
	return s.writeCommittedReply(client, netproto.MsgChatAccepted, netproto.ChatAccepted{
		ClientMsgID: msg.ClientMsgID, ChannelID: msg.ChannelID,
		ToUniqueID: msg.ToUniqueID, ToClientID: msg.ToClientID,
		Disposition: disposition, MessageID: messageID,
	})
}

// echoAcceptedDirect writes acknowledged own messages before acceptance so a
// full broadcast queue cannot silently discard the sender's local DM history.
// A socket failure leaves the outcome unknown; it must never trigger a resend.
func (s *TCPServer) echoAcceptedDirect(client *Client, msg netproto.ChatSend, payload []byte) error {
	if msg.AckRequested {
		return s.writeCommittedReply(client, netproto.MsgEvent, json.RawMessage(payload))
	}
	_ = s.deps.Broadcast.BroadcastToClient(client.ID, payload)
	return nil
}
