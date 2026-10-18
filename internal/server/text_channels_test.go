package server

import (
	"context"
	"errors"
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

// textChannelLifecycleEnv starts a server whose lifecycle requests reach the
// validation and storage path (Authority plus an in-memory channel writer),
// with voice channel 1 and text channel 2. Actor 2 is the policy owner.
func textChannelLifecycleEnv(t *testing.T) (*testEnv, *memoryRoleChannels) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var writer *memoryRoleChannels
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = a
		writer = &memoryRoleChannels{ChannelBackend: d.Channels, backend: backend}
		d.Channels = writer
	})
	env.state.AddChannel(testChannel(1))
	text := testChannel(2)
	text.TextOnly = true
	env.state.AddChannel(text)
	return env, writer
}

// Text-only is a creation attribute: it cannot be temporary (it would be
// pruned at once, nobody can occupy it) and no other operation may set it.
func TestRoleChannelChangeValidatesTextOnly(t *testing.T) {
	env, writer := textChannelLifecycleEnv(t)
	defer env.stop()
	settings := &netproto.RoleChannelSettings{Name: "general"}
	for name, request := range map[string]netproto.RoleChannelChange{
		"temporary text channel": {Kind: authorization.ChannelCreate, ExpectedRevision: 1, Settings: settings, ChannelType: 0, TextOnly: true},
		"edit sets text only":    {Kind: authorization.ChannelEdit, ExpectedRevision: 1, ChannelID: 1, Settings: settings, TextOnly: true},
		"move sets text only":    {Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 1, TextOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := env.srv.changeRoleChannel(t.Context(), 2, request, nil); !errors.Is(err, authorization.ErrRoleInvalid) {
				t.Fatalf("invalid text-only change: %v, want ErrRoleInvalid", err)
			}
		})
	}
	if writer.created != nil || writer.edited != nil {
		t.Fatalf("rejected change reached storage: created=%+v edited=%+v", writer.created, writer.edited)
	}
}

// A text channel is reached through chat subscriptions, which never check a
// channel password, so a password would only pretend to protect it: neither
// creating a passworded text channel nor setting a password on an existing
// one is accepted. Clearing one, and passwords on voice channels, still work.
func TestRoleChannelChangeRejectsTextChannelPasswords(t *testing.T) {
	env, writer := textChannelLifecycleEnv(t)
	defer env.stop()
	settings := &netproto.RoleChannelSettings{Name: "general"}
	for name, request := range map[string]netproto.RoleChannelChange{
		"create passworded text channel": {Kind: authorization.ChannelCreate, ExpectedRevision: 1, Settings: settings, ChannelType: 2, TextOnly: true, Password: "secret"},
		"set password on text channel":   {Kind: authorization.ChannelEdit, ExpectedRevision: 1, ChannelID: 2, Settings: settings, SetPassword: true, Password: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := env.srv.changeRoleChannel(t.Context(), 2, request, nil); !errors.Is(err, authorization.ErrRoleInvalid) {
				t.Fatalf("passworded text channel change: %v, want ErrRoleInvalid", err)
			}
		})
	}
	if writer.created != nil || writer.edited != nil {
		t.Fatalf("rejected change reached storage: created=%+v edited=%+v", writer.created, writer.edited)
	}
	result, err := env.srv.changeRoleChannel(t.Context(), 2, netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ExpectedRevision: 1, ChannelID: 2, Settings: settings, SetPassword: true}, nil)
	if err != nil || writer.edited == nil || writer.edited.PasswordHash == nil || *writer.edited.PasswordHash != "" {
		t.Fatalf("clearing a text channel password: %+v %+v %v", result, writer.edited, err)
	}
	if _, err := env.srv.changeRoleChannel(t.Context(), 2, netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ExpectedRevision: result.Revision, ChannelID: 1, Settings: settings, SetPassword: true, Password: "secret"}, nil); err != nil {
		t.Fatalf("voice channel password: %v", err)
	}
	if _, err := env.srv.changeRoleChannel(t.Context(), 2, netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: result.Revision + 1, Settings: settings, ChannelType: 2, TextOnly: true}, nil); err != nil || writer.created == nil || !writer.created.TextOnly || writer.created.PasswordHash != "" {
		t.Fatalf("plain text channel create: %+v %v", writer.created, err)
	}
}
