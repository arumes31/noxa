package server

import (
	"noxa/internal/netproto"
	"testing"
)

func TestVoiceQueueAuthorizationAndAcknowledgement(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	admin, adminID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = admin.Close() }()
	user, userID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = user.Close() }()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	send(t, admin, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "voice membership", func() bool {
		a, _ := env.state.GetClient(adminID)
		b, _ := env.state.GetClient(userID)
		return a.ChannelID == 1 && b.ChannelID == 1
	})
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	var result netproto.VoiceQueueResult
	if err := netproto.Decode(readOfType(t, user, netproto.MsgVoiceQueueResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.ClientID != userID || result.ChannelID != 1 || result.Action != "raise" {
		t.Fatal(result)
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "clear", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	member, _ := env.state.GetClient(userID)
	if member.HandRaisedOrder == 0 {
		t.Fatal("member cleared queue")
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1, ClientID: adminID})
	readOfType(t, user, netproto.MsgError)
	send(t, admin, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "next", ChannelID: 1})
	if err := netproto.Decode(readOfType(t, admin, netproto.MsgVoiceQueueResult), &result); err != nil {
		t.Fatal(err)
	}
	member, _ = env.state.GetClient(userID)
	if !member.HandCalled || member.HandRaisedOrder != 0 || member.ServerMuted || member.SelfMuted {
		t.Fatalf("bad call: %+v", member)
	}
	if err := env.state.MoveClient(userID, 2); err != nil {
		t.Fatal(err)
	}
	send(t, user, netproto.MsgVoiceQueueRequest, netproto.VoiceQueueRequest{Action: "raise", ChannelID: 1})
	readOfType(t, user, netproto.MsgError)
	member, _ = env.state.GetClient(userID)
	if member.HandRaisedOrder != 0 || member.HandCalled {
		t.Fatal("stale action crossed channel")
	}
}
