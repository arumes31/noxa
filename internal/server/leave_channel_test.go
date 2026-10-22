package server

import (
	"testing"

	"noxa/internal/netproto"
)

func TestJoinZeroLeavesChannelWithoutDisconnecting(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	user, userID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = user.Close() }()
	env.state.AddChannel(testChannel(1))
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user joined", func() bool {
		client, ok := env.state.GetClient(userID)
		return ok && client.ChannelID == 1
	})
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 0})
	waitFor(t, "user left but remains connected", func() bool {
		client, ok := env.state.GetClient(userID)
		return ok && client.ChannelID == 0 && len(env.state.ChannelMembers(1)) == 0
	})
	// Leaving again is harmless; the same connection can rejoin.
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 0})
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "user rejoined", func() bool {
		client, ok := env.state.GetClient(userID)
		return ok && client.ChannelID == 1
	})
}

func TestHomeAnchorReturnsToNeutralMembership(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	user, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = user.Close() }()
	home := testChannel(2)
	home.SystemKind = "home"
	env.state.AddChannel(home)
	env.state.AddChannel(testChannel(1))
	member, _ := env.state.GetClient(id)
	if member.ChannelID != 0 {
		t.Fatal("new client is not home")
	}
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	waitFor(t, "voice joined", func() bool { member, _ := env.state.GetClient(id); return member.ChannelID == 1 })
	send(t, user, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	waitFor(t, "home reached", func() bool {
		member, _ := env.state.GetClient(id)
		return member.ChannelID == 0 && len(env.state.ChannelMembers(1)) == 0
	})
	if len(env.state.ChannelMembers(2)) != 0 {
		t.Fatal("home accidentally became a voice channel")
	}
}
