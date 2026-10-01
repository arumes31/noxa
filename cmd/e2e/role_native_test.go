package main

import (
	"errors"
	"testing"

	"noxa/internal/netproto"
)

func TestRoleChatRequiresCorrelatedEncryptedTraffic(t *testing.T) {
	key := [32]byte{8}
	body := "matching body is not sufficient"
	blob, err := e2eSealScope(body, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "other channel", "other account", "other client", "other reference", "direct", "plaintext", "missing stored id", "unknown key", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			receiver := &client{conn: conn, scopeKeys: map[int64]map[uint32][32]byte{7: {3: key}}}
			sender := &client{uid: "sender-uid", clientID: "sender-client"}
			chat := netproto.ChatBroadcast{ID: 42, ChannelID: "7", FromUniqueID: sender.uid, FromClientID: sender.clientID, ClientMsgID: "ref", Enc: true, KeyID: 3, Text: blob}
			switch kind {
			case "other channel":
				chat.ChannelID = "8"
			case "other account":
				chat.FromUniqueID = "other"
			case "other client":
				chat.FromClientID = "other"
			case "other reference":
				chat.ClientMsgID = "other"
			case "direct":
				chat.Direct = true
			case "plaintext":
				chat.Text, chat.Enc = body, false
			case "missing stored id":
				chat.ID = 0
			case "unknown key":
				chat.KeyID = 4
			case "corrupt":
				chat.Text = "broken"
			}
			queueDeadlineTestFrame(t, conn, netproto.MsgEvent, map[string]any{"type": "chat", "data": chat})
			_, err := readRoleChat(receiver, sender, 7, "ref", body)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("correlation/encryption validation = %v", err)
			}
		})
	}
}

type failingRoleWriteConn struct{ deadlineRecordingConn }

func (*failingRoleWriteConn) Write([]byte) (int, error) { return 0, errors.New("uncertain write") }

func TestRoleDeniedNativeWriteStopsOwnerCleanup(t *testing.T) {
	queryConn := &deadlineRecordingConn{}
	s := &roleScenario{query: &querySession{conn: queryConn, r: &lineReader{conn: queryConn}}, revision: 8}
	if err := checkDeniedRoleChannelDelete(s, &client{conn: &failingRoleWriteConn{}}, 7); err == nil {
		t.Fatal("write error ignored")
	}
	if !s.halted {
		t.Fatal("uncertain native write left cleanup enabled")
	}
	if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: "channel_delete", ChannelID: 7}); err == nil {
		t.Fatal("owner cleanup proceeded after uncertain native write")
	}
	if queryConn.output.Len() != 0 {
		t.Fatal("owner mutation sent after uncertain native write")
	}
}

func TestRoleChatRejectionDoesNotAcceptAnEchoBeforeTheError(t *testing.T) {
	conn := &deadlineRecordingConn{}
	cl := &client{conn: conn, uid: "sender", clientID: "session"}
	request := netproto.ChatSend{ClientMsgID: "rejected-ref"}
	queueDeadlineTestFrame(t, conn, netproto.MsgEvent, map[string]any{"type": "chat", "data": netproto.ChatBroadcast{ClientMsgID: request.ClientMsgID, FromUniqueID: cl.uid}})
	queueDeadlineTestFrame(t, conn, netproto.MsgError, netproto.Error{Code: 4, OriginType: uint16(netproto.MsgChatSend)})
	if err := readRoleChatRejection(cl, request, 4, ""); err == nil {
		t.Fatal("delivered message counted as rejected")
	}
}

func TestRoleChatRejectionObserverDoesNotSkipForbiddenReference(t *testing.T) {
	conn := &deadlineRecordingConn{}
	cl := &client{conn: conn, uid: "observer", clientID: "observer-session"}
	queueDeadlineTestFrame(t, conn, netproto.MsgEvent, map[string]any{"type": "chat", "data": netproto.ChatBroadcast{ClientMsgID: "forbidden", FromUniqueID: "other-sender"}})
	if _, err := readRoleChat(cl, cl, 7, "marker", "body", "forbidden"); err == nil || err.Error() != "rejected native chat reached the observer" {
		t.Fatalf("forbidden recipient traffic skipped: %v", err)
	}
}

func TestRoleChatHistoryFenceRejectsPostErrorEcho(t *testing.T) {
	conn := &deadlineRecordingConn{}
	cl := &client{conn: conn}
	queueDeadlineTestFrame(t, conn, netproto.MsgEvent, map[string]any{"type": "chat", "data": netproto.ChatBroadcast{ClientMsgID: "forbidden"}})
	queueDeadlineTestFrame(t, conn, netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{ChannelID: 7})
	if _, err := roleScenarioHistory(cl, 7, "forbidden"); err == nil {
		t.Fatal("post-error echo discarded before history fence")
	}
}

func TestRoleNativeErrorRequiresExpectedOperation(t *testing.T) {
	for _, kind := range []string{"valid", "wrong code", "wrong origin", "wrong detail"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			result := netproto.Error{Code: 2, OriginType: uint16(netproto.MsgChatSend), Message: "slow mode: wait"}
			switch kind {
			case "wrong code":
				result.Code = 5
			case "wrong origin":
				result.OriginType = uint16(netproto.MsgChatDelete)
			case "wrong detail":
				result.Message = "invalid message"
			}
			queueDeadlineTestFrame(t, conn, netproto.MsgError, result)
			err := expectRoleNativeError(conn, netproto.MsgChatSend, 2, "slow mode")
			if (err == nil) != (kind == "valid") {
				t.Fatalf("error correlation = %v", err)
			}
		})
	}
}
