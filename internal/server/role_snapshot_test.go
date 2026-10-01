package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestRoleSnapshotHidesResourcesCountsAndParentReferences(t *testing.T) {
	sm := state.New(testLogger())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "private-parent", ClientCount: 20})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "public-child"})
	sm.AddChannel(&state.Channel{ChannelID: 3, ParentID: 1, Name: "private-child"})
	policy := serverRoleFixture().policy
	policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	policy.Channels = []authorization.ChannelPolicy{
		{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}},
		{ChannelID: 2, ParentID: 1},
		{ChannelID: 3, ParentID: 1, Synced: true},
	}
	e, err := authorization.NewRoleEvaluator(policy)
	if err != nil {
		t.Fatal(err)
	}
	view := buildRoleSnapshot(sm, e, 1, "viewer")
	if view.TotalChannels != 1 || view.TotalClients != 0 || len(view.RootChannels) != 1 || view.RootChannels[0].ParentID != 0 {
		t.Fatalf("unexpected filtered view: %+v", view)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") {
		t.Fatalf("private resource leaked: %s", raw)
	}
	if got := buildRoleSnapshot(sm, e, 2, "owner"); got.TotalChannels != 3 {
		t.Fatalf("owner channels = %d", got.TotalChannels)
	}
}

func TestRoleLoginSnapshotCarriesAuthenticatedMemberCosmetics(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Member", Position: 1, Color: "#abcdef"})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "pw", AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	readOfType(t, conn, netproto.MsgAuthResponse)
	frame := readOfType(t, conn, netproto.MsgSnapshot)
	var snapshot broadcast.TreeSnapshot
	if err := netproto.Decode(frame, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.UnassignedClients) != 1 || len(snapshot.UnassignedClients[0].Roles) != 1 || snapshot.UnassignedClients[0].Roles[0].Name != "Member" {
		t.Fatalf("login lost authenticated appearance: %+v", snapshot.UnassignedClients)
	}
	if strings.Contains(string(frame.Payload), "user_id") {
		t.Fatal("internal account id was serialized")
	}
}

func TestRoleSnapshotCosmeticsFollowVisibilityAndCurrentAssignments(t *testing.T) {
	sm := state.New(testLogger())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "visible"})
	sm.AddChannel(&state.Channel{ChannelID: 2, Name: "hidden"})
	sm.AddClient(&state.Client{ClientID: "visible", UserID: 4, UniqueID: "member", ChannelID: 1})
	sm.AddClient(&state.Client{ClientID: "hidden", UserID: 5, UniqueID: "hidden-member", ChannelID: 2})
	p := serverRoleFixture().policy
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	p.Roles = append(p.Roles,
		authorization.Role{ID: 20, Name: "Helper", Position: 1, Color: "#abcdef", Icon: "★", Hoist: true, Permissions: []authorization.Capability{authorization.Speak}},
		authorization.Role{ID: 30, Name: "Hidden role", Position: 2})
	p.Members = []authorization.RoleMember{{UserID: 4, RoleIDs: []int64{20}}, {UserID: 5, RoleIDs: []int64{30}}}
	p.Channels = append(p.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	for _, removed := range []bool{false, true} {
		if removed {
			p.Members = nil
			p.Revision++
		}
		e, err := authorization.NewRoleEvaluator(p)
		if err != nil {
			t.Fatal(err)
		}
		view := buildRoleSnapshot(sm, e, 1, "viewer")
		raw, err := json.Marshal(view)
		if err != nil || strings.Contains(string(raw), "Hidden role") || strings.Contains(string(raw), "hidden-member") || strings.Contains(string(raw), "user_id") || strings.Contains(string(raw), "permissions") {
			t.Fatalf("snapshot cosmetics leaked private data: %s, %v", raw, err)
		}
		roles := view.RootChannels[0].Clients[0].Roles
		if removed && len(roles) != 0 || !removed && (len(roles) != 1 || roles[0].Name != "Helper" || roles[0].Icon != "★") {
			t.Fatalf("snapshot ignored current assignments: %+v", roles)
		}
	}
}
