package server

import (
	"testing"

	"noxa/internal/netproto"
)

func TestJoinZeroLeavesChannelWithoutDisconnecting(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	admin, _ := dialAuthed(t, env.addr, "admin-uid")
	defer admin.Close()
	user, userID := dialAuthed(t, env.addr, "user-uid")
	defer user.Close()
	send(t, admin, netproto.MsgCreateChannel, netproto.CreateChannel{Name: "Voice", Type: 2})
	readOfType(t, admin, netproto.MsgChannelList)
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
