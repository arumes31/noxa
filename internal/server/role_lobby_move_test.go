package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestLobbyMembersCannotBeMoved(t *testing.T) {
	for _, actor := range []string{"owner", "moderator", "integration", "self"} {
		for _, source := range []int64{0, 3} {
			t.Run(actor+map[int64]string{0: "/lobby", 3: "/home"}[source], func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
				backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2}, authorization.ChannelPolicy{ChannelID: 3, SystemKind: "home"})
				if actor == "moderator" {
					backend.policy.OwnerID = 3
					backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Mover", Position: 1, Permissions: []authorization.Capability{authorization.MoveMembers}})
					backend.policy.Members = []authorization.RoleMember{{UserID: 2, RoleIDs: []int64{20}}}
				}
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
				defer env.stop()
				voice := testChannel(1)
				voice.Name = "Lobby" // Display names do not confer lobby protection.
				env.state.AddChannel(voice)
				env.state.AddChannel(testChannel(2))
				home := testChannel(3)
				home.SystemKind = "home"
				env.state.AddChannel(testChannel(3))
				sender, senderID := dialAuthed(t, env.addr, "user-uid")
				defer func() { _ = sender.Close() }()
				memberConn, memberID := dialAuthed(t, env.addr, "admin-uid")
				defer func() { _ = memberConn.Close() }()
				if actor == "self" {
					memberID = senderID
				}
				if source != 0 {
					if err := env.state.MoveClient(memberID, source); err != nil {
						t.Fatal(err)
					}
				}
				env.state.AddChannel(home)
				before, _ := env.state.GetClient(memberID)
				request := netproto.MoveClient{ClientID: memberID, ChannelID: 1, AckRequested: true}
				if actor == "integration" {
					evaluator, err := authorization.NewRoleEvaluator(backend.policy)
					if err != nil {
						t.Fatal(err)
					}
					if err := env.srv.moveRoleMember(t.Context(), evaluator, 2, "user-uid", "", request); !errors.Is(err, authorization.ErrRoleForbidden) {
						t.Fatalf("integration moved lobby member: %v", err)
					}
				} else {
					send(t, sender, netproto.MsgMoveClient, request)
					send(t, sender, netproto.MsgPing, netproto.Ping{})
					if err := sender.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
						t.Fatal(err)
					}
					got := readMemberActionFence(sender, nil)
					if got.err != nil || len(got.moved) != 0 || len(got.errors) != 1 || got.errors[0].Code != errCodePermissionDenied || got.errors[0].OriginType != uint16(netproto.MsgMoveClient) {
						t.Fatalf("expected correlated denial without success acknowledgement: %+v", got)
					}
				}
				after, _ := env.state.GetClient(memberID)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("denied move changed member: before=%+v after=%+v", before, after)
				}
				member, _ := env.srv.clientByID(memberID)
				evaluator, err := authorization.NewRoleEvaluator(backend.policy)
				if err != nil {
					t.Fatal(err)
				}
				if env.srv.roleSnapshotWithOwnMove(member, evaluator).OwnChannelMove != nil {
					t.Fatal("denied move recorded a move cause")
				}
				if actor == "self" {
					return
				}
				// Lobby members must still be able to join on their own.
				send(t, memberConn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, AckRequested: true})
				readOfType(t, memberConn, netproto.MsgChannelJoined)
				// Moderation is allowed again after voluntary voice admission.
				if err := env.srv.moveRoleMember(t.Context(), evaluator, 2, "user-uid", "", netproto.MoveClient{ClientID: memberID, ChannelID: 2}); err != nil {
					t.Fatal(err)
				}
				if err := env.srv.moveRoleMember(t.Context(), evaluator, 2, "user-uid", "", netproto.MoveClient{ClientID: memberID, ChannelID: 3}); err != nil {
					t.Fatal(err)
				}
				if current, _ := env.state.GetClient(memberID); current.ChannelID != 0 {
					t.Fatal("member did not return to lobby")
				}
			})
		}
	}
}
