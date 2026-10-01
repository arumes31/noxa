package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type memoryRoleChannels struct {
	ChannelBackend
	backend *memoryRoleStore
	created *store.RoleChannelCreate
	edited  *store.RoleChannelSettings
	change  authorization.ChannelTreeChange
}

func TestRoleChannelMoveCarriesExplicitOrderToWriter(t *testing.T) {
	backend := serverRoleFixture()
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
	defer env.stop()
	order := int32(0)
	result, err := env.srv.changeRoleChannel(t.Context(), backend.policy.OwnerID, netproto.RoleChannelChange{
		Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 1, OrderIndex: &order,
	}, nil)
	if err != nil || result.Revision != 2 || writer.change.OrderIndex == nil || *writer.change.OrderIndex != 0 {
		t.Fatalf("move: %+v %+v %v", result, writer.change, err)
	}
	_, err = env.srv.changeRoleChannel(t.Context(), backend.policy.OwnerID, netproto.RoleChannelChange{
		Kind: authorization.ChannelEdit, ExpectedRevision: 2, ChannelID: 1, OrderIndex: &order, Settings: &netproto.RoleChannelSettings{Name: "Invalid extra field"},
	}, nil)
	if !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
	if p, err := a.RolePolicy(t.Context()); err != nil || p.Revision != 2 {
		t.Fatalf("rejected order changed authority: %+v %v", p, err)
	}
}

func TestChannelLifecycleRechecksAdmissionAfterCreatePreflight(t *testing.T) {
	backend := serverRoleFixture()
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
	defer env.stop()
	calls := 0
	_, err = env.srv.changeRoleChannel(t.Context(), 2, netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ChannelType: 2, Settings: &netproto.RoleChannelSettings{Name: "Denied after preflight"}, Password: "channel-password"}, func(context.Context) error {
		calls++
		if calls == 2 {
			return auth.ErrIntegrationDenied
		}
		return nil
	})
	if !errors.Is(err, auth.ErrIntegrationDenied) || calls != 2 {
		t.Fatalf("admission checks=%d err=%v", calls, err)
	}
	if writer.created != nil {
		t.Fatal("revoked identity committed a channel")
	}
	policy, err := a.RolePolicy(t.Context())
	if err != nil || policy.Revision != 1 {
		t.Fatalf("denial poisoned or changed policy: %+v %v", policy, err)
	}
}

func (m *memoryRoleChannels) ChangeRoleChannel(_ context.Context, actor int64, change authorization.ChannelTreeChange, create *store.RoleChannelCreate) (authorization.RolePolicy, int64, error) {
	m.backend.mu.Lock()
	defer m.backend.mu.Unlock()
	if change.Kind == authorization.ChannelCreate {
		change.ChannelID = 1
		for _, ch := range m.backend.policy.Channels {
			if ch.ChannelID >= change.ChannelID {
				change.ChannelID = ch.ChannelID + 1
			}
		}
		change.Access.ChannelID = change.ChannelID
	}
	p, err := authorization.ApplyChannelTreeChange(m.backend.policy, actor, change)
	if err == nil {
		m.backend.policy = p
		m.created = create
		m.change = change
	}
	return p, change.ChannelID, err
}

func (m *memoryRoleChannels) EditRoleChannel(ctx context.Context, actor, channelID, revision int64, settings store.RoleChannelSettings) (authorization.RolePolicy, error) {
	p, _, err := m.ChangeRoleChannel(ctx, actor, authorization.ChannelTreeChange{Kind: authorization.ChannelEdit, ChannelID: channelID, ExpectedRevision: revision}, nil)
	if err == nil {
		m.edited = &settings
	}
	return p, err
}

func TestRoleChannelQueryFiltersVisibilitySubtreeAndDestinations(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ManageChannels}
	backend.policy.Channels = []authorization.ChannelPolicy{{ChannelID: 1}, {ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}, {ChannelID: 3, ParentID: 1}}
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
	defer env.stop()
	for _, id := range []int64{1, 2, 3} {
		env.state.AddChannel(testChannel(id))
	}
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRoleChannelQuery, netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: 2})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("hidden lookup: %+v", got)
	}
	send(t, conn, netproto.MsgRoleChannelQuery, netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: 1})
	var result netproto.RoleChannelState
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChannelState), &result); err != nil {
		t.Fatal(err)
	}
	if result.AffectedChannels != 2 || len(result.Destinations) != 1 || result.Destinations[0].ID != 0 || result.Destinations[0].CanSync {
		t.Fatalf("unsafe destinations or count: %+v", result)
	}
	// A hidden custom descendant must prevent a subtree count response.
	_, err = a.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1,
		Channel: authorization.ChannelPolicy{ChannelID: 3, ParentID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgRoleChannelQuery, netproto.RoleChannelQuery{Kind: authorization.ChannelDelete, ChannelID: 1})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("hidden subtree lookup: %+v", got)
	}
}

func TestRoleChannelTemporaryCreatorAndAcknowledgements(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.CreateTemporaryChannels}
	var writer *memoryRoleChannels
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = a
		writer = &memoryRoleChannels{ChannelBackend: d.Channels, backend: backend}
		d.Channels = writer
	})
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRoleChannelQuery, netproto.RoleChannelQuery{Kind: authorization.ChannelCreate})
	var options netproto.RoleChannelState
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChannelState), &options); err != nil {
		t.Fatal(err)
	}
	if options.CanCreatePermanent || !options.CanCreateTemporary || options.CanManageAccess || len(options.Roles) != 0 {
		t.Fatalf("temporary creator options: %+v", options)
	}
	request := netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ChannelType: 2, Settings: &netproto.RoleChannelSettings{Name: "Room"}}
	send(t, conn, netproto.MsgRoleChannelChange, request)
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("permanent create: %+v", got)
	}
	request.ChannelType = 256
	send(t, conn, netproto.MsgRoleChannelChange, request)
	if got := readError(t, conn); got.Code != errCodeMalformed {
		t.Fatalf("wrapped channel type: %+v", got)
	}
	request.ChannelType = 0
	request.Settings.Name = "invalid\x00name"
	send(t, conn, netproto.MsgRoleChannelChange, request)
	if got := readError(t, conn); got.Code != errCodeMalformed {
		t.Fatalf("NUL create: %+v", got)
	}
	if _, err := a.RolePolicy(t.Context()); err != nil {
		t.Fatalf("malformed settings closed authority: %v", err)
	}
	request.Settings.Name = "Room"
	request.Access = &netproto.RoleChannelAccess{Synced: false}
	send(t, conn, netproto.MsgRoleChannelChange, request)
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("custom access create: %+v", got)
	}
	request.Access = nil
	request.Password = "channel-password-canary"
	send(t, conn, netproto.MsgRoleChannelChange, request)
	var result netproto.RoleChannelResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChannelResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || result.ChannelID != 2 || result.EnforcementPending {
		t.Fatalf("create ack: %+v", result)
	}
	backend.mu.Lock()
	created := *writer.created
	backend.mu.Unlock()
	if created.PasswordHash == request.Password || auth.VerifyPassword(request.Password, created.PasswordHash) != nil {
		t.Fatal("channel password was not prepared as a hash")
	}
	send(t, conn, netproto.MsgRoleChannelChange, request)
	if got := readError(t, conn); got.Code != errCodeConflict {
		t.Fatalf("stale create: %+v", got)
	}
}

func TestRoleChannelEditAcknowledgesSavedEnforcementFailure(t *testing.T) {
	backend := serverRoleFixture()
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error {
		return errors.New("reconciliation failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = a
		d.Channels = &memoryRoleChannels{ChannelBackend: d.Channels, backend: backend}
	})
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgRoleChannelChange, netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ExpectedRevision: 1, ChannelID: 1, Settings: &netproto.RoleChannelSettings{Name: "Updated", MaxClients: 5}})
	var result netproto.RoleChannelResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgRoleChannelResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || result.ChannelID != 1 || !result.EnforcementPending {
		t.Fatalf("saved edit: %+v", result)
	}
	if _, err := a.RolePolicy(t.Context()); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatal("failed enforcement reopened access")
	}
}
