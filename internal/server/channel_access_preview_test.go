package server

import (
	"context"
	"reflect"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestChannelTreePreviewUsesAuthorityWithoutCreatingResources(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.Speak, Effect: authorization.Deny}}})
	reconciled := 0
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		reconciled++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := authority.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Roles = backend })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, tt := range []struct {
		name      string
		tree      authorization.ChannelTreeChange
		channelID int64
		changes   int
	}{
		{"create", authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ParentID: 1, Access: authorization.ChannelPolicy{ParentID: 1, Synced: true}}, 0, 3},
		{"keep", authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 1, ParentID: 2}, 1, 0},
		{"sync", authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 1, ParentID: 2, SyncToParent: true}, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			send(t, owner, netproto.MsgChannelAccessPreview, netproto.ChannelAccessPreview{Tree: &tt.tree, UserIDs: []int64{0}})
			var response authorization.ChannelAccessImpact
			if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelAccessImpact), &response); err != nil {
				t.Fatal(err)
			}
			if response.Revision != 1 || response.ChannelID != tt.channelID || len(response.Members) != 1 || len(response.Members[0].Changes) != tt.changes {
				t.Fatalf("preview: %+v", response)
			}
		})
	}
	request := netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ParentID: 1, Access: authorization.ChannelPolicy{ParentID: 1, Synced: true}}, UserIDs: []int64{0}}
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgChannelAccessPreview, request)
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("unauthorized tree preview: %+v", got)
	}
	request.Change.Kind = authorization.ChannelAccessSet
	send(t, owner, netproto.MsgChannelAccessPreview, request)
	if got := readError(t, owner); got.Code != errCodeMalformed {
		t.Fatalf("mixed preview: %+v", got)
	}
	current, err := authority.RolePolicy(t.Context())
	if err != nil || !reflect.DeepEqual(original, current) || reconciled != 0 {
		t.Fatalf("preview changed authority: %+v, %v, reconciled %d", current, err, reconciled)
	}
}

func TestChannelAccessPreviewUsesCurrentAuthorityAndNeverCommits(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	reconciled := 0
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		reconciled++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Roles = backend })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	request := netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1,
		Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}}, UserIDs: []int64{0, 1, 2}}
	send(t, owner, netproto.MsgChannelAccessPreview, request)
	var response authorization.ChannelAccessImpact
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelAccessImpact), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revision != 1 || response.ChannelID != 1 || len(response.Members) != 3 || len(response.Members[0].Changes) != 3 || len(response.Members[2].Changes) != 0 {
		t.Fatalf("preview: %+v", response)
	}
	p, err := authority.RolePolicy(t.Context())
	if err != nil || p.Revision != 1 || len(p.Channels[0].Overrides) != 0 || reconciled != 0 {
		t.Fatalf("preview committed: %+v, %v, reconciled %d", p, err, reconciled)
	}
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgChannelAccessPreview, request)
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin preview: %+v", got)
	}
	for _, channelID := range []int64{1, 999} {
		request.Change.Channel.ChannelID = channelID
		request.Change.ExpectedRevision = 2
		send(t, owner, netproto.MsgChannelAccessPreview, request)
		want := uint16(errCodeConflict)
		if channelID == 999 {
			want = errCodePermissionDenied
		}
		if got := readError(t, owner); got.Code != want {
			t.Fatalf("invalid preview: %+v", got)
		}
	}
}

func TestChannelAccessPreviewRequiresAuthority(t *testing.T) {
	output := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
	client := &Client{ID: "member", Conn: output}
	client.setIdentity("user-uid", "User", 2, false)
	frame, err := netproto.Encode(netproto.MsgChannelAccessPreview, netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 1}}, UserIDs: []int64{0}})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, testLogger(), &Deps{})
	if err := srv.handleChannelAccessPreview(t.Context(), client, frame); err != nil {
		t.Fatal(err)
	}
	if got := readSessionError(t, output); got.Code != errCodeUnavailable {
		t.Fatalf("store-only preview: %+v", got)
	}
}

func TestChannelAccessPreviewScopesStayWithinManagedSyncedDescendants(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	backend.policy.Channels = append(backend.policy.Channels,
		authorization.ChannelPolicy{ChannelID: 2, ParentID: 1, Synced: true},
		authorization.ChannelPolicy{ChannelID: 3, ParentID: 1},
		authorization.ChannelPolicy{ChannelID: 4, ParentID: 3, Synced: true})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	send(t, owner, netproto.MsgRoleQuery, netproto.RoleQuery{ChannelID: 1})
	var state netproto.RoleState
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgRoleState), &state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.ImpactChannelIDs, []int64{2}) || len(state.Policy.Channels) != 1 {
		t.Fatalf("scope disclosure: %+v", state)
	}
	for _, ch := range backend.policy.Channels {
		resource := testChannel(ch.ChannelID)
		resource.ParentID = ch.ParentID
		env.state.AddChannel(resource)
	}
	send(t, owner, netproto.MsgRoleChannelQuery, netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: 1})
	var channelState netproto.RoleChannelState
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgRoleChannelState), &channelState); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelState.ImpactChannelIDs, []int64{2}) {
		t.Fatalf("move scope disclosure: %+v", channelState)
	}
	request := netproto.ChannelAccessPreview{ScopeChannelID: 2, Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1,
		Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}}, UserIDs: []int64{0}}
	send(t, owner, netproto.MsgChannelAccessPreview, request)
	var response authorization.ChannelAccessImpact
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelAccessImpact), &response); err != nil {
		t.Fatal(err)
	}
	if response.ChannelID != 2 || response.Revision != 1 || len(response.Members) != 1 || len(response.Members[0].Changes) != 3 {
		t.Fatalf("descendant response: %+v", response)
	}
	for _, id := range []int64{3, 4, 999} {
		request.ScopeChannelID = id
		send(t, owner, netproto.MsgChannelAccessPreview, request)
		if got := readError(t, owner); got.Code != errCodePermissionDenied {
			t.Fatalf("scope %d: %+v", id, got)
		}
	}
}

func TestChannelAccessPreviewObservesRevocation(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Speak, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Manager", Position: 1, Permissions: []authorization.Capability{authorization.ManageChannelAccess}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	request := netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1,
		Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.Speak, Effect: authorization.Deny}}}}, UserIDs: []int64{0}}
	send(t, member, netproto.MsgChannelAccessPreview, request)
	readOfType(t, member, netproto.MsgChannelAccessImpact)
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 20, Name: "Manager", Position: 1}}); err != nil {
		t.Fatal(err)
	}
	request.Change.ExpectedRevision = 2
	send(t, member, netproto.MsgChannelAccessPreview, request)
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked grant preview: %+v", got)
	}
	client, output := revokedRequestClient(env)
	frame, err := netproto.Encode(netproto.MsgChannelAccessPreview, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.dispatch(t.Context(), client, frame); err != nil {
		t.Fatal(err)
	}
	if got := readSessionError(t, output); got.Code != errCodeNotAuthenticated {
		t.Fatalf("revoked owner session preview: %+v", got)
	}
	// A request that already passed dispatch authentication must also fail at
	// the policy lease after session revocation.
	if err := env.srv.rolePolicyRead(t.Context(), client, func(ctx context.Context) error {
		return env.srv.handleChannelAccessPreview(ctx, client, frame)
	}); err != nil {
		t.Fatal(err)
	}
	if got := readSessionError(t, output); got.Code != errCodePermissionDenied {
		t.Fatalf("queued revoked preview: %+v", got)
	}
}
