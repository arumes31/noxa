package server

import (
	"context"
	"reflect"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// A text channel carries chat through subscriptions only: nobody joins it for
// voice (not even with the password, not by a moderator move), while a
// member entitled to view it still subscribes and receives its chat key.
func TestTextChannelRefusesVoiceButSubscribes(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	text := testChannel(2)
	text.TextOnly = true
	text.PasswordHash, err = auth.HashPassword("text-password")
	if err != nil {
		t.Fatal(err)
	}
	env.state.AddChannel(text)

	pub, _ := testX25519(t)
	member, memberAuth := dialSubscriptionClient(t, env.addr, "admin-uid", pub)
	defer func() { _ = member.Close() }()
	for _, password := range []string{"", "text-password"} {
		send(t, member, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2, Password: password})
		if got := readError(t, member); got.Code != errCodePermissionDenied || got.Message != state.ErrTextChannel.Error() {
			t.Fatalf("voice join of a text channel (password %q) = %+v", password, got)
		}
	}
	if ch, _, _ := env.state.ClientChannelState(memberAuth.ClientID); ch != 0 {
		t.Fatalf("refused join moved the member to %d", ch)
	}

	send(t, member, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{2}})
	reply, keys, errs := readSubscriptionReply(t, member)
	if !reflect.DeepEqual(reply.ChannelIDs, []int64{2}) || len(errs) != 0 || len(keys) != 1 || keys[0].ChannelID != 2 {
		t.Fatalf("text channel subscription = %+v keys=%+v errors=%+v", reply, keys, errs)
	}

	// Not even the owner, who bypasses passwords and may move anyone, can
	// place a member (or itself) into a text channel.
	moderator, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = moderator.Close() }()
	send(t, member, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	readChannelKeyFor(t, member, 1)
	send(t, moderator, netproto.MsgMoveClient, netproto.MoveClient{ClientID: memberAuth.ClientID, ChannelID: 2})
	if got := readError(t, moderator); got.Code == 0 {
		t.Fatalf("moderator move into a text channel = %+v", got)
	}
	if ch, _, _ := env.state.ClientChannelState(memberAuth.ClientID); ch != 1 {
		t.Fatalf("member channel after refused move = %d, want 1", ch)
	}
	send(t, moderator, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	if got := readError(t, moderator); got.Code != errCodePermissionDenied {
		t.Fatalf("owner voice join of a text channel = %+v", got)
	}
}

// Text-only is a creation attribute: it cannot be temporary (it would be
// pruned at once, nobody can occupy it) and no other operation may set it.
func TestRoleChannelChangeValidatesTextOnly(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	settings := &netproto.RoleChannelSettings{Name: "general"}
	for name, request := range map[string]netproto.RoleChannelChange{
		"temporary text channel": {Kind: authorization.ChannelCreate, Settings: settings, ChannelType: 0, TextOnly: true},
		"edit sets text only":    {Kind: authorization.ChannelEdit, ChannelID: 1, Settings: settings, TextOnly: true},
		"move sets text only":    {Kind: authorization.ChannelMove, ChannelID: 1, TextOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := env.srv.changeRoleChannel(t.Context(), 1, request, nil); err == nil {
				t.Fatal("accepted an invalid text-only change")
			}
		})
	}
}
