package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestRoleMovementRechecksActiveMediaFlags(t *testing.T) {
	for _, lifecycle := range []bool{false, true} {
		for _, scenario := range []struct {
			name                                string
			destination                         int64
			moderated, denied, full, wantActive bool
		}{
			{name: "denied join", destination: 2, denied: true},
			{name: "denied moderator move", destination: 2, moderated: true, denied: true},
			{name: "allowed join", destination: 2, wantActive: true},
			{name: "same channel", destination: 1, wantActive: true},
			{name: "leave"},
			{name: "rejected full channel", destination: 2, denied: true, full: true, wantActive: true},
		} {
			t.Run(scenario.name+map[bool]string{false: "/state", true: "/lifecycle"}[lifecycle], func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.PrioritySpeaker, authorization.ShareScreen}
				destination := authorization.ChannelPolicy{ChannelID: 2}
				if scenario.denied {
					destination.Overrides = []authorization.RoleOverride{
						{RoleID: 10, Capability: authorization.PrioritySpeaker, Effect: authorization.Deny},
						{RoleID: 10, Capability: authorization.ShareScreen, Effect: authorization.Deny},
					}
				}
				backend.policy.Channels = append(backend.policy.Channels, destination)
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					d.Authority = authority
					if !lifecycle {
						d.Channels = nil
					}
				})
				defer env.stop()
				env.state.AddChannel(testChannel(1))
				channel := testChannel(2)
				if scenario.full {
					channel.MaxClients = 1
				}
				env.state.AddChannel(channel)
				if scenario.full {
					env.state.AddClient(&state.Client{ClientID: "occupant"})
					if err := env.state.MoveClient("occupant", 2); err != nil {
						t.Fatal(err)
					}
				}
				conn, id := dialAuthed(t, env.addr, "admin-uid")
				defer func() { _ = conn.Close() }()
				if err := env.state.MoveClient(id, 1); err != nil {
					t.Fatal(err)
				}
				send(t, conn, netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: true})
				send(t, conn, netproto.MsgScreenShare, netproto.ScreenShare{Active: true})
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				readOfType(t, conn, netproto.MsgPong)
				before, _ := env.state.GetClient(id)
				if !before.PrioritySpeaker || !before.Sharing {
					t.Fatal("source media activation failed")
				}
				if scenario.moderated {
					owner, _ := dialAuthed(t, env.addr, "user-uid")
					defer func() { _ = owner.Close() }()
					send(t, owner, netproto.MsgMoveClient, netproto.MoveClient{ClientID: id, ChannelID: scenario.destination})
					send(t, owner, netproto.MsgPing, netproto.Ping{})
					readOfType(t, owner, netproto.MsgPong)
				} else {
					send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: scenario.destination})
					send(t, conn, netproto.MsgPing, netproto.Ping{})
					readOfType(t, conn, netproto.MsgPong)
				}
				after, _ := env.state.GetClient(id)
				wantChannel := scenario.destination
				if scenario.full {
					wantChannel = 1
				}
				if after.ChannelID != wantChannel || after.PrioritySpeaker != scenario.wantActive || after.Sharing != scenario.wantActive {
					t.Fatalf("destination media state: %+v; channel=%d active=%v", after, wantChannel, scenario.wantActive)
				}
			})
		}
	}
}

func TestRoleSnapshotRejectsStaleMediaFlags(t *testing.T) {
	p := serverRoleFixture().policy
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(testLogger())
	sm.AddChannel(testChannel(1))
	sm.AddClient(&state.Client{ClientID: "member", UserID: 1, ChannelID: 1, PrioritySpeaker: true, Sharing: true})
	sm.AddClient(&state.Client{ClientID: "owner-lobby", UserID: 2, PrioritySpeaker: true, Sharing: true})
	view := buildRoleSnapshot(sm, e, p.OwnerID, "owner")
	for _, member := range append(view.RootChannels[0].Clients, view.UnassignedClients...) {
		if member.PrioritySpeaker || member.Sharing {
			t.Fatalf("stale media exposed: %+v", member)
		}
	}
}

func TestRoleQueuedMediaFlagsRequireCurrentAuthority(t *testing.T) {
	for _, kind := range []string{eventPrioritySpeakerChanged, eventScreenshareChanged} {
		for _, tc := range []struct {
			name                                  string
			user, channel                         int64
			stored, active, allowed, wantDelivery bool
		}{
			{name: "cleared flag", user: 1, channel: 1, active: true, allowed: true},
			{name: "stale denied flag", user: 1, channel: 1, stored: true, active: true},
			{name: "owner lobby", user: 2, stored: true, active: true, allowed: true},
			{name: "authorized flag", user: 1, channel: 1, stored: true, active: true, allowed: true, wantDelivery: true},
			{name: "owner channel", user: 2, channel: 1, stored: true, active: true, wantDelivery: true},
			{name: "disable after denial", user: 1, channel: 1, wantDelivery: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				p := serverRoleFixture().policy
				p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
				if tc.allowed {
					p.Roles[0].Permissions = append(p.Roles[0].Permissions, authorization.PrioritySpeaker, authorization.ShareScreen)
				}
				e, err := authorization.NewRoleEvaluator(p)
				if err != nil {
					t.Fatal(err)
				}
				sm := state.New(testLogger())
				sm.AddChannel(testChannel(1))
				sm.AddClient(&state.Client{ClientID: "subject", UserID: tc.user, ChannelID: tc.channel, PrioritySpeaker: tc.stored, Sharing: tc.stored})
				srv := &TCPServer{deps: &Deps{State: sm}}
				payload, err := eventEnvelope(kind, map[string]any{"client_id": "subject", "channel_id": tc.channel, "active": tc.active})
				if err != nil {
					t.Fatal(err)
				}
				frame, err := srv.roleBroadcastFrame(&Client{UserID: p.OwnerID}, payload, e)
				if err != nil || (frame != nil) != tc.wantDelivery {
					t.Fatalf("delivery=%v want=%v error=%v", frame != nil, tc.wantDelivery, err)
				}
			})
		}
	}
}
