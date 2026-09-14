package server

import (
	"encoding/json"
	"net"
	"testing"

	"voicx/internal/config"
	"voicx/internal/netproto"
)

func TestDirectMessagesReachOnlineGuestsByUniqueID(t *testing.T) {
	for _, tc := range []struct {
		name           string
		senderGuest    bool
		recipientGuest bool
	}{
		{name: "guest to guest", senderGuest: true, recipientGuest: true},
		{name: "registered to guest", recipientGuest: true},
		{name: "guest to registered", senderGuest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := startTestEnvFull(t, nil, func(cfg *config.Config) { cfg.ChatAllowPlaintext = false })
			defer env.stop()
			connect := func(nickname string, guest bool) (net.Conn, string) {
				t.Helper()
				if !guest {
					conn, _ := dialAuthed(t, env.addr, "user-uid")
					return conn, "user-uid"
				}
				conn := dialRetry(t, env.addr)
				send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Anonymous: true, Nickname: nickname})
				var response netproto.AuthResponse
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
					t.Fatal(err)
				}
				if !response.OK || response.UniqueID == "" {
					t.Fatalf("guest auth failed: %+v", response)
				}
				readOfType(t, conn, netproto.MsgSnapshot)
				return conn, response.UniqueID
			}
			sender, senderUID := connect("BRAVO", tc.senderGuest)
			defer func() { _ = sender.Close() }()
			recipient, recipientUID := connect("ALPHA", tc.recipientGuest)
			defer func() { _ = recipient.Close() }()
			ciphertext := "opaque-ciphertext-" + tc.name
			send(t, sender, netproto.MsgChatSend, netproto.ChatSend{
				ToUniqueID: recipientUID, Text: ciphertext, Enc: true, ClientMsgID: "guest-dm",
			})
			// Sender echo also serves as a bounded error observation: reject the
			// original not-found error immediately rather than timing out later.
			for _, conn := range []net.Conn{sender, recipient} {
				for {
					frame := readFrame(t, conn)
					if netproto.MessageType(frame.Type) == netproto.MsgError {
						var response netproto.Error
						if err := netproto.Decode(frame, &response); err != nil {
							t.Fatal(err)
						}
						t.Fatalf("online DM rejected: %+v", response)
					}
					if netproto.MessageType(frame.Type) != netproto.MsgEvent {
						continue
					}
					typ, data := decodeEvent(t, frame)
					if typ != eventChat {
						continue
					}
					var message netproto.ChatBroadcast
					if err := json.Unmarshal(data, &message); err != nil {
						t.Fatal(err)
					}
					if message.Text != ciphertext || message.FromUniqueID != senderUID || message.ToUniqueID != recipientUID || !message.Direct || !message.E2E || !message.Enc || message.EncVerified || message.ClientMsgID != "guest-dm" {
						t.Fatalf("wrong direct relay: %+v", message)
					}
					break
				}
			}
			if env.spool.pendingCount() != 0 {
				t.Fatal("online direct message was incorrectly spooled")
			}
		})
	}
}

func TestDirectMessageUnknownOfflineIdentityRemainsNotFound(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "unknown-offline", Text: "opaque", Enc: true})
	var response netproto.Error
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != errCodeNotFound || env.spool.pendingCount() != 0 {
		t.Fatalf("unknown offline target accepted or spooled: %+v", response)
	}
}
