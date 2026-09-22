package server

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"slices"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestRoleMentionsRespectSenderVisibilityAndGrant(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		actor                      int64
		mass, stats, channelDenied bool
	}{
		{name: "ordinary", actor: 1},
		{name: "mass grant", actor: 1, mass: true},
		{name: "statistics grant", actor: 1, mass: true, stats: true},
		{name: "channel denies mass grant", actor: 1, mass: true, channelDenied: true},
		{name: "owner", actor: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}
			if tc.mass {
				backend.policy.Roles[0].Permissions = append(backend.policy.Roles[0].Permissions, authorization.MentionEveryone)
			}
			if tc.stats {
				backend.policy.Roles[0].Permissions = append(backend.policy.Roles[0].Permissions, authorization.ViewConnectionInfo)
			}
			if tc.channelDenied {
				backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.MentionEveryone, Effect: authorization.Deny}}
			}
			backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
			a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			sm := state.New(testLogger())
			sm.AddChannel(testChannel(1))
			sm.AddChannel(testChannel(2))
			for _, member := range []*state.Client{
				{ClientID: "visible", UserID: 3, UniqueID: "visible-uid", Nickname: "visible"},
				{ClientID: "invisible", UserID: 4, UniqueID: "invisible-uid", Nickname: "invisible", Status: "invisible"},
				{ClientID: "hidden", UserID: 5, UniqueID: "hidden-uid", Nickname: "hidden"},
			} {
				sm.AddClient(member)
				channel := int64(1)
				if member.ClientID == "hidden" {
					channel = 2
				}
				if err := sm.MoveClient(member.ClientID, channel); err != nil {
					t.Fatal(err)
				}
			}
			srv := &TCPServer{deps: &Deps{Authority: a, State: sm}}
			sender := &Client{UserID: tc.actor, UniqueID: "sender-uid"}
			for _, scope := range []int64{0, 1} {
				for _, body := range []string{"@everyone", "@here", "@channel", "@visible @invisible @hidden"} {
					want := []string{}
					if body == "@visible @invisible @hidden" || (tc.mass && (scope != 1 || !tc.channelDenied)) || tc.actor == 2 {
						want = append(want, "visible-uid")
						if tc.stats || tc.actor == 2 {
							want = append(want, "invisible-uid")
						}
						if tc.actor == 2 && (scope == 0 || body == "@visible @invisible @hidden") {
							want = append(want, "hidden-uid")
						}
					}
					if err := srv.withRolePolicy(t.Context(), func(ctx context.Context) error {
						got := srv.parseMentions(ctx, sender, scope, body)
						slices.Sort(got)
						slices.Sort(want)
						if !slices.Equal(got, want) {
							t.Errorf("scope=%d body=%s mentions=%v want=%v", scope, body, got, want)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestRoleQueuedChatMentionsDiscloseOnlyRecipient(t *testing.T) {
	p := serverRoleFixture().policy
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, direct := range []bool{false, true} {
		chat := netproto.ChatBroadcast{ChannelID: "1", Direct: direct, ToUniqueID: "visible", Text: "ciphertext", Enc: true, EncVerified: true, Offline: true, KeyID: 3, ID: 7, ReplyToID: 2, Version: 4, ClientMsgID: "reference", FromUniqueID: "author", Mentions: []string{"visible", "hidden", "invisible", "visible"}}
		payload, err := eventEnvelope(eventChat, chat)
		if err != nil {
			t.Fatal(err)
		}
		if !direct {
			payload, err = eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: 1, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, uid := range []string{"visible", "hidden", "invisible", "observer", ""} {
			// Ordinary recipients and the owner get the same minimal metadata.
			srv := &TCPServer{}
			actor := int64(1)
			if uid == "observer" {
				actor = p.OwnerID
			}
			frame, err := srv.roleBroadcastFrame(&Client{UserID: actor, UniqueID: uid}, payload, e)
			if err != nil || frame == nil {
				t.Fatalf("frame=%v error=%v", frame, err)
			}
			var envelope struct {
				Data netproto.ChatBroadcast `json:"data"`
			}
			if err := json.Unmarshal(frame.Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			want := chat
			want.Mentions = nil
			if slices.Contains(chat.Mentions, uid) {
				want.Mentions = []string{uid}
			}
			if !reflect.DeepEqual(envelope.Data, want) {
				t.Fatalf("recipient=%q chat=%+v want=%+v", uid, envelope.Data, want)
			}
		}
	}
}

func TestRoleMentionsOverTCPAndLiveRevocation(t *testing.T) {
	backend := serverRoleFixture()
	base := []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}
	backend.policy.Roles[0].Permissions = append(append([]authorization.Capability(nil), base...), authorization.MentionEveryone)
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
	defer env.stop()
	env.state.AddChannel(testChannel(2))
	env.state.AddClient(&state.Client{ClientID: "private", UserID: 9, UniqueID: "hidden-canary", Nickname: "private", ChannelID: 2})
	env.state.AddClient(&state.Client{ClientID: "invisible", UserID: 8, UniqueID: "invisible-canary", Nickname: "invisible", Status: "invisible"})
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	keyID, key, err := env.srv.chatKeys.EnsureScope(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		conn                  net.Conn
		name, body, mentioned string
	}{
		{owner, "user", "@everyone owner notice", "admin-uid"},
		{member, "admin", "@here member notice", "user-uid"},
		{member, "admin", "@channel revoked notice", ""},
	} {
		if step.mentioned == "" {
			if _, err := a.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: base}}); err != nil {
				t.Fatal(err)
			}
		}
		sendEncChat(t, step.conn, key, keyID, "", step.body)
		for _, recipient := range []struct {
			conn net.Conn
			uid  string
		}{{member, "admin-uid"}, {owner, "user-uid"}} {
			data := readChatFrom(t, recipient.conn, step.name)
			var chat netproto.ChatBroadcast
			if err := json.Unmarshal(data, &chat); err != nil {
				t.Fatal(err)
			}
			want := []string{}
			if recipient.uid == step.mentioned {
				want = append(want, recipient.uid)
			}
			if !slices.Equal(chat.Mentions, want) {
				t.Fatalf("recipient=%s mentions=%v want=%v", recipient.uid, chat.Mentions, want)
			}
			plain, err := env.srv.chatKeys.open(t.Context(), 0, chat.KeyID, chat.Text)
			if err != nil || plain != step.body {
				t.Fatalf("chat body changed: %q %v", plain, err)
			}
		}
	}
}
