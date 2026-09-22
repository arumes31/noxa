package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

type gatedChatSendStore struct {
	*fakeChat
	before func() error
}

func (s *gatedChatSendStore) StoreChatMessage(ctx context.Context, channelID int64, uid, name, body string, key uint32, reply int64, ref string) (int64, bool, error) {
	if err := s.before(); err != nil {
		return 0, false, err
	}
	return s.fakeChat.StoreChatMessage(ctx, channelID, uid, name, body, key, reply, ref)
}

type gatedChatSpool struct {
	*fakeSpool
	before func() error
}

func (s *gatedChatSpool) SpoolMessage(ctx context.Context, from, to int64, uid, body string) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.fakeSpool.SpoolMessage(ctx, from, to, uid, body)
}

type chatSendFence struct {
	accepted []netproto.ChatAccepted
	echoes   []netproto.ChatBroadcast
	errors   []netproto.Error
	err      error
}

func readChatSendFence(conn net.Conn, accepted chan<- struct{}) chatSendFence {
	var result chatSendFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgChatAccepted:
			var a netproto.ChatAccepted
			result.err = netproto.Decode(f, &a)
			result.accepted = append(result.accepted, a)
			if accepted != nil {
				accepted <- struct{}{}
			}
		case netproto.MsgError:
			var failure netproto.Error
			result.err = netproto.Decode(f, &failure)
			result.errors = append(result.errors, failure)
		case netproto.MsgEvent:
			var event struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			result.err = netproto.Decode(f, &event)
			if result.err == nil && event.Type == eventChat {
				var chat netproto.ChatBroadcast
				result.err = json.Unmarshal(event.Data, &chat)
				result.echoes = append(result.echoes, chat)
			}
		case netproto.MsgPong:
			return result
		}
		if result.err != nil {
			return result
		}
	}
}

func chatSendTestAuthority(t *testing.T) *authorization.Authority {
	t.Helper()
	a, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestChatSendAcknowledgementFollowsStorage(t *testing.T) {
	for _, scope := range []string{"global", "channel", "offline"} {
		for _, outcome := range []string{"saved", "failed", "denied", "unrequested"} {
			if scope == "offline" && outcome == "denied" {
				continue
			}
			t.Run(scope+"/"+outcome, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				before := func() error {
					close(entered)
					<-release
					if outcome == "failed" {
						return errors.New("storage unavailable")
					}
					return nil
				}
				storage := &gatedChatSendStore{fakeChat: newFakeChat(), before: before}
				spool := &gatedChatSpool{fakeSpool: &fakeSpool{}, before: before}
				authority := chatSendTestAuthority(t)
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = storage; d.Spool = spool })
				defer env.stop()
				defer unblock()
				uid := "user-uid"
				if outcome == "denied" {
					uid = "admin-uid"
				}
				conn, _ := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				msg := netproto.ChatSend{Text: "hello", ClientMsgID: "reference", AckRequested: outcome != "unrequested"}
				switch scope {
				case "channel":
					env.state.AddChannel(testChannel(1))
					if outcome != "denied" {
						pub, _ := testX25519(t)
						publishKey(t, conn, pub)
						send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
						readChannelKeyFor(t, conn, 1)
					}
					msg.ChannelID = "1"
				case "offline":
					msg.ToUniqueID, msg.Enc, msg.Text = "admin-uid", true, "opaque-dm-ciphertext"
				}
				send(t, conn, netproto.MsgChatSend, msg)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				done, accepted := make(chan chatSendFence, 1), make(chan struct{}, 4)
				go func() { done <- readChatSendFence(conn, accepted) }()
				if outcome != "denied" {
					select {
					case <-entered:
					case got := <-done:
						t.Fatalf("did not reach storage: %+v", got)
					case <-time.After(time.Second):
						t.Fatal("no storage call")
					}
					select {
					case <-accepted:
						t.Fatal("accepted before storage completed")
					case got := <-done:
						t.Fatalf("completed before storage: %+v", got)
					case <-time.After(20 * time.Millisecond):
					}
					unblock()
				}
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				if outcome == "saved" {
					if len(got.accepted) != 1 || !got.accepted[0].Matches(msg) || len(got.errors) != 0 {
						t.Fatalf("acceptance: %+v", got)
					}
					if scope == "offline" && (got.accepted[0].Disposition != netproto.ChatQueued || len(got.echoes) != 1 || got.echoes[0].Text != msg.Text || got.echoes[0].ClientMsgID != msg.ClientMsgID) {
						t.Fatalf("offline acceptance: %+v", got)
					}
				} else if len(got.accepted) != 0 {
					t.Fatalf("unexpected acceptance: %+v", got)
				}
				if outcome == "failed" || outcome == "denied" {
					if len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgChatSend) || len(got.echoes) != 0 {
						t.Fatalf("failure: %+v", got)
					}
				} else if len(got.errors) != 0 {
					t.Fatalf("unexpected error: %+v", got)
				}
				count := storage.messageCount()
				if scope == "offline" {
					pending, err := spool.PendingMessages(t.Context(), 1)
					if err != nil {
						t.Fatal(err)
					}
					count = len(pending)
				}
				want := 0
				if outcome == "saved" || outcome == "unrequested" {
					want = 1
				}
				if count != want {
					t.Fatalf("stored %d, want %d", count, want)
				}
			})
		}
	}
}

func TestChatSendOfflineEchoSurvivesFullQueue(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	const sender = "stalled-sender"
	if _, err := env.deps.Broadcast.Register(sender); err != nil {
		t.Fatal(err)
	}
	defer env.deps.Broadcast.Unregister(sender)
	for {
		err := env.deps.Broadcast.BroadcastToClient(sender, []byte("queued"))
		if errors.Is(err, broadcast.ErrChannelFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	server, conn := net.Pipe()
	defer func() { _ = server.Close(); _ = conn.Close() }()
	client := &Client{ID: sender, Conn: server, UserID: 2, UniqueID: "user-uid"}
	msg := netproto.ChatSend{AckRequested: true, ClientMsgID: "ref", ToUniqueID: "admin-uid", Enc: true, Text: "ciphertext"}
	payload, err := eventEnvelope(eventChat, netproto.ChatBroadcast{Direct: true, ToUniqueID: msg.ToUniqueID, Text: msg.Text, Enc: true, ClientMsgID: msg.ClientMsgID})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- env.srv.sendDirectByUniqueID(t.Context(), client, msg, payload) }()
	f := readFrame(t, conn)
	if netproto.MessageType(f.Type) != netproto.MsgEvent {
		t.Fatalf("acceptance preceded own echo: %s", netproto.MessageType(f.Type))
	}
	var accepted netproto.ChatAccepted
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgChatAccepted), &accepted); err != nil {
		t.Fatal(err)
	}
	if !accepted.Matches(msg) {
		t.Fatalf("wrong acceptance: %+v", accepted)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChatSendStoredDuplicateAcknowledgement(t *testing.T) {
	a := chatSendTestAuthority(t)
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	msg := netproto.ChatSend{AckRequested: true, ClientMsgID: "ref", Text: "hello"}
	var first netproto.ChatAccepted
	for i := 0; i < 2; i++ {
		send(t, conn, netproto.MsgChatSend, msg)
		var got netproto.ChatAccepted
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgChatAccepted), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Matches(msg) {
			t.Fatalf("invalid acceptance: %+v", got)
		}
		if i == 0 {
			first = got
		} else if first != got {
			t.Fatalf("duplicate: %+v, first: %+v", got, first)
		}
	}
	if count := env.chat.messageCount(); count != 1 {
		t.Fatalf("duplicate stored %d messages", count)
	}
}

func TestChatSendLiveDirectAcknowledgement(t *testing.T) {
	for _, destination := range []string{"unique", "client"} {
		t.Run(destination, func(t *testing.T) {
			a := chatSendTestAuthority(t)
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
			defer env.stop()
			sender, _ := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = sender.Close() }()
			recipient, recipientID := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = recipient.Close() }()
			msg := netproto.ChatSend{AckRequested: true, ClientMsgID: "ref", Text: "opaque ciphertext", Enc: true}
			if destination == "unique" {
				msg.ToUniqueID = "admin-uid"
			} else {
				msg.ToClientID = recipientID
			}
			send(t, sender, netproto.MsgChatSend, msg)
			send(t, sender, netproto.MsgPing, netproto.Ping{})
			if err := sender.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			got := readChatSendFence(sender, nil)
			if got.err != nil || len(got.errors) != 0 || len(got.accepted) != 1 || !got.accepted[0].Matches(msg) || got.accepted[0].Disposition != netproto.ChatRelayed {
				t.Fatalf("acceptance: %+v", got)
			}
			if len(got.echoes) != 1 || got.echoes[0].ClientMsgID != msg.ClientMsgID || got.echoes[0].ToUniqueID != "admin-uid" || got.echoes[0].Text != msg.Text || !got.echoes[0].E2E {
				t.Fatalf("own echo: %+v", got)
			}
			var delivered netproto.ChatBroadcast
			if err := json.Unmarshal(readEventOfType(t, recipient, eventChat), &delivered); err != nil {
				t.Fatal(err)
			}
			if delivered.ClientMsgID != msg.ClientMsgID || delivered.Text != msg.Text || delivered.FromUniqueID != "user-uid" || delivered.ToUniqueID != "admin-uid" || !delivered.E2E {
				t.Fatalf("recipient event differs: %+v", delivered)
			}
			pending, err := env.spool.PendingMessages(t.Context(), 1)
			if err != nil || len(pending) != 0 || env.chat.messageCount() != 0 {
				t.Fatal("live direct message stored")
			}
		})
	}
}

func TestChatSendAcknowledgementRejectsInvalidRequests(t *testing.T) {
	for _, failure := range []string{"empty reference", "long reference", "invalid channel", "negative channel", "zero channel", "mixed scope", "both recipients", "direct reply", "missing store", "missing recipient", "plaintext offline", "missing spool"} {
		t.Run(failure, func(t *testing.T) {
			a := chatSendTestAuthority(t)
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				d.Authority = a
				if failure == "missing store" {
					d.Chat = nil
				}
				if failure == "missing spool" {
					d.Spool = nil
				}
			})
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = conn.Close() }()
			msg := netproto.ChatSend{AckRequested: true, ClientMsgID: "ref", Text: "hello"}
			switch failure {
			case "empty reference":
				msg.ClientMsgID = ""
			case "long reference":
				msg.ClientMsgID = string(make([]byte, 129))
			case "invalid channel":
				msg.ChannelID = "invalid"
			case "negative channel":
				msg.ChannelID = "-1"
			case "zero channel":
				msg.ChannelID = "0"
			case "mixed scope":
				msg.ChannelID, msg.ToUniqueID = "1", "admin-uid"
			case "both recipients":
				msg.ToUniqueID, msg.ToClientID = "admin-uid", "target"
			case "direct reply":
				msg.ToUniqueID, msg.ReplyToID = "admin-uid", 1
			case "missing recipient":
				msg.ToClientID = "missing"
			case "plaintext offline":
				msg.ToUniqueID = "admin-uid"
			case "missing spool":
				msg.ToUniqueID, msg.Enc = "admin-uid", true
			}
			send(t, conn, netproto.MsgChatSend, msg)
			send(t, conn, netproto.MsgPing, netproto.Ping{})
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			got := readChatSendFence(conn, nil)
			if got.err != nil || len(got.accepted) != 0 || len(got.echoes) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgChatSend) {
				t.Fatalf("failure: %+v", got)
			}
			pending, err := env.spool.PendingMessages(t.Context(), 1)
			if err != nil || len(pending) != 0 || env.chat.messageCount() != 0 {
				t.Fatal("invalid request stored")
			}
		})
	}
}
