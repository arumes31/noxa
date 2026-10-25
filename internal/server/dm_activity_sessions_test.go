package server

import (
	"encoding/json"
	"net"
	"testing"

	"noxa/internal/netproto"
)

// A DM typing indicator and both receipts reach every session of the
// addressed account, so a phone sees them while the desktop is online too.
func TestDMActivityReachesEverySessionOfTheAccount(t *testing.T) {
	env, _ := activityTestEnv(t, false)
	defer env.stop()
	desktop, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = desktop.Close() }()
	phone, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = phone.Close() }()
	contact, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = contact.Close() }()

	send(t, contact, netproto.MsgTyping, netproto.Typing{ToUniqueID: "user-uid"})
	for _, conn := range []net.Conn{desktop, phone} {
		var event struct {
			UniqueID string `json:"unique_id"`
			Direct   bool   `json:"direct"`
		}
		if err := json.Unmarshal(readEventOfType(t, conn, eventTyping), &event); err != nil {
			t.Fatal(err)
		}
		if event.UniqueID != "admin-uid" || !event.Direct {
			t.Fatalf("typing event=%+v", event)
		}
	}
	send(t, contact, netproto.MsgChatDelivered, netproto.ChatDelivered{ToUniqueID: "user-uid", ClientMsgID: "m-1"})
	send(t, contact, netproto.MsgChatRead, netproto.ChatRead{ToUniqueID: "user-uid", ClientMsgID: "m-1"})
	for _, conn := range []net.Conn{desktop, phone} {
		for _, kind := range []string{eventDMDelivered, eventDMRead} {
			var event struct {
				From        string `json:"from_unique_id"`
				ClientMsgID string `json:"client_msg_id"`
			}
			if err := json.Unmarshal(readEventOfType(t, conn, kind), &event); err != nil {
				t.Fatal(err)
			}
			if event.From != "admin-uid" || event.ClientMsgID != "m-1" {
				t.Fatalf("%s event=%+v", kind, event)
			}
		}
	}
}
