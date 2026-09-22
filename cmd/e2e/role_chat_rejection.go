package main

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"noxa/internal/netproto"
)

func checkRoleRejectedChat(cl, observer *client, channelID int64, code uint16, detail string) error {
	before, err := roleScenarioHistory(cl, channelID)
	if err != nil {
		return err
	}
	request, err := sendRoleChat(cl, channelID, "must-not-deliver-"+randHex(12))
	if err != nil {
		return err
	}
	if err := readRoleChatRejection(cl, request, code, detail); err != nil {
		return err
	}
	// This request follows the error on the same ordered control stream. A
	// rejected send must leave no new message, edit or tombstone in its scope.
	after, err := roleScenarioHistory(cl, channelID, request.ClientMsgID)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(before.Messages, after.Messages, func(a, b netproto.ChatHistoryEntry) bool {
		return a.ID == b.ID && a.Version == b.Version && a.Deleted == b.Deleted && a.BodyEnc == b.BodyEnc && a.KeyID == b.KeyID
	}) {
		return errors.New("rejected native chat changed stored history")
	}
	// The marker is sent after the denial and history fence. The observer's
	// ordered broadcast queue must reach it without any rejected message ref.
	body := "rejection-fence-" + randHex(12)
	marker, err := sendRoleChat(observer, channelID, body)
	if err != nil {
		return err
	}
	_, err = readRoleChat(observer, observer, channelID, marker.ClientMsgID, body, request.ClientMsgID)
	return err
}

func roleScenarioHistory(cl *client, channelID int64, forbiddenRefs ...string) (netproto.ChatHistoryResponse, error) {
	var result netproto.ChatHistoryResponse
	if err := writeMsg(cl.conn, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: channelID, Limit: 100}); err != nil {
		return result, err
	}
	f, err := readRoleRejectionFrame(cl, netproto.MsgChatHistoryResponse, forbiddenRefs...)
	if err != nil {
		return result, err
	}
	if err := netproto.Decode(f, &result); err != nil {
		return result, err
	}
	if result.ChannelID != channelID || len(result.Messages) >= 100 {
		return result, errors.New("rejection check requires the complete isolated test-channel history")
	}
	for _, entry := range result.Messages {
		if entry.Body != "" {
			return result, errors.New("server returned plaintext history")
		}
	}
	installScopeKeys(cl, channelID, result.Keys, false)
	return result, nil
}

func readRoleChatRejection(cl *client, request netproto.ChatSend, code uint16, detail string) error {
	f, err := readRoleRejectionFrame(cl, netproto.MsgError, request.ClientMsgID)
	if err != nil {
		return err
	}
	var result netproto.Error
	if err := netproto.Decode(f, &result); err != nil {
		return err
	}
	return validateRoleNativeError(result, netproto.MsgChatSend, code, detail)
}

// The same observer spans the error and subsequent history fence so neither
// synchronous nor already-queued asynchronous rejected echoes are discarded.
func readRoleRejectionFrame(cl *client, want netproto.MessageType, forbiddenRefs ...string) (*netproto.Frame, error) {
	if err := cl.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	defer clearE2EReadDeadline(cl.conn)
	for {
		f, err := netproto.ReadFrame(cl.conn)
		if err != nil {
			return nil, err
		}
		if netproto.MessageType(f.Type) == want {
			return f, nil
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgPing:
			if err := writeMsg(cl.conn, netproto.MsgPong, netproto.Pong{}); err != nil {
				return nil, err
			}
		case netproto.MsgChannelKey:
			captureChannelKey(cl.conn, f)
		case netproto.MsgEvent:
			var event eventEnvelope
			if err := netproto.Decode(f, &event); err != nil {
				return nil, err
			}
			if event.Type != "chat" {
				continue
			}
			var chat netproto.ChatBroadcast
			if err := json.Unmarshal(event.Data, &chat); err != nil {
				return nil, err
			}
			if slices.Contains(forbiddenRefs, chat.ClientMsgID) {
				return nil, errors.New("rejected native chat was broadcast")
			}
		case netproto.MsgError:
			return nil, errors.New("unexpected native error while fencing rejected chat")
		}
	}
}
