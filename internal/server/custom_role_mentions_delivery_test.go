package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"slices"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestCustomRoleMentionsTCPDelivery(t *testing.T) {
	for _, scope := range []int64{0, 1} {
		t.Run(fmt.Sprintf("scope_%d", scope), func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}
			backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Raid team", Position: 1, Mentionable: true})
			backend.policy.Members = []authorization.RoleMember{
				{UserID: 1, RoleIDs: []int64{20}}, {UserID: 2, RoleIDs: []int64{20}}, {UserID: 3, RoleIDs: []int64{20}},
			}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			// The owner can resolve this invisible member, but its identity must
			// never appear in notification metadata sent to another recipient.
			env.state.AddClient(&state.Client{ClientID: "invisible-canary", UserID: 3, UniqueID: "invisible-canary-uid", Status: "invisible"})
			memberPub, _ := testX25519(t)
			member, memberInfo := dialSubscriptionClient(t, env.addr, "admin-uid", memberPub)
			defer func() { _ = member.Close() }()
			ownerPub, _ := testX25519(t)
			owner, ownerInfo := dialSubscriptionClient(t, env.addr, "user-uid", ownerPub)
			defer func() { _ = owner.Close() }()
			if scope != 0 {
				for _, conn := range []net.Conn{member, owner} {
					send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{scope}})
					response, keys, errs := readSubscriptionReply(t, conn)
					if !slices.Equal(response.ChannelIDs, []int64{scope}) || len(keys) != 1 || len(errs) != 0 {
						t.Fatalf("subscribe: %+v %+v %+v", response, keys, errs)
					}
				}
			}
			for _, id := range []string{memberInfo.ClientID, ownerInfo.ClientID} {
				client, ok := env.state.GetClient(id)
				if !ok || client.ChannelID != 0 {
					t.Fatal("chat-only recipient unexpectedly joined voice")
				}
			}
			keyID, key, err := env.srv.chatKeys.EnsureScope(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			channel := ""
			if scope != 0 {
				channel = fmt.Sprint(scope)
			}
			for _, revoked := range []bool{false, true} {
				if revoked {
					if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: 1, UserID: 1}); err != nil {
						t.Fatal(err)
					}
				}
				body := fmt.Sprintf("Ready <@&20>? @admin revoked=%t", revoked)
				sendEncChat(t, owner, key, keyID, channel, body)
				for _, recipient := range []struct {
					conn net.Conn
					uid  string
				}{{member, "admin-uid"}, {owner, "user-uid"}} {
					var chat netproto.ChatBroadcast
					if err := json.Unmarshal(readChatFrom(t, recipient.conn, "user"), &chat); err != nil {
						t.Fatal(err)
					}
					var wantRoles, wantDirect []string
					if recipient.uid == "admin-uid" {
						wantDirect = []string{recipient.uid}
						if !revoked {
							wantRoles = []string{recipient.uid}
						}
					}
					if !slices.Equal(chat.RoleMentions, wantRoles) || !slices.Equal(chat.Mentions, wantDirect) {
						t.Fatalf("recipient=%s revoked=%t role mentions=%v want=%v direct mentions=%v want=%v", recipient.uid, revoked, chat.RoleMentions, wantRoles, chat.Mentions, wantDirect)
					}
					plain, err := env.srv.chatKeys.open(t.Context(), scope, chat.KeyID, chat.Text)
					if err != nil || plain != body {
						t.Fatalf("chat body changed: %q %v", plain, err)
					}
				}
			}
		})
	}
}

func TestCustomRoleMentionsQueuedDeliveryIsRecipientOnly(t *testing.T) {
	p := serverRoleFixture().policy
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	evaluator, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, scoped := range []bool{false, true} {
		chat := netproto.ChatBroadcast{ChannelID: "1", Text: "ciphertext", Enc: true, EncVerified: true, KeyID: 8, ID: 9, ReplyToID: 4, Version: 2, ClientMsgID: "reference", FromUniqueID: "author", Mentions: []string{"direct", "both", "both"}, RoleMentions: []string{"role", "both", "hidden", "both", ""}}
		if !scoped {
			chat.ChannelID = ""
		}
		payload, err := eventEnvelope(eventChat, chat)
		if err != nil {
			t.Fatal(err)
		}
		var channelID int64
		if scoped {
			channelID = 1
		}
		payload, err = eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: channelID, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		for _, uid := range []string{"direct", "role", "both", "hidden", "observer", ""} {
			srv := &TCPServer{}
			// The owner must not receive a larger notification roster either.
			actor := int64(1)
			if uid == "observer" {
				actor = p.OwnerID
			}
			frame, err := srv.roleBroadcastFrame(&Client{UserID: actor, UniqueID: uid}, payload, evaluator)
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
			want.Mentions, want.RoleMentions = nil, nil
			if uid != "" && slices.Contains(chat.Mentions, uid) {
				want.Mentions = []string{uid}
			}
			if uid != "" && slices.Contains(chat.RoleMentions, uid) {
				want.RoleMentions = []string{uid}
			}
			if !reflect.DeepEqual(envelope.Data, want) {
				t.Fatalf("scoped=%t recipient=%q chat=%+v want=%+v", scoped, uid, envelope.Data, want)
			}
		}
	}
}
