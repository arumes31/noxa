package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestDelegatedServerConfigurationUsesCurrentGrant(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = authority
	})
	defer env.stop()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	query := func() { send(t, member, netproto.MsgServerConfigQuery, netproto.ServerConfigQuery{}) }
	query()
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("ungranted read = %+v", got)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ManageServer}}}); err != nil {
		t.Fatal(err)
	}
	query()
	var initial netproto.ServerConfig
	if err := netproto.Decode(readOfType(t, member, netproto.MsgServerConfigResponse), &initial); err != nil {
		t.Fatal(err)
	}
	want := netproto.ServerConfig{MaxClients: 75, ClientTimeoutSeconds: 90, OpusBitrate: 64000, OpusFEC: true, OpusStereo: true}
	send(t, member, netproto.MsgServerConfigSet, want)
	var saved netproto.ServerConfig
	if err := netproto.Decode(readOfType(t, member, netproto.MsgServerConfigResponse), &saved); err != nil || saved != want {
		t.Fatalf("delegated save = %+v, %v", saved, err)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2,
		Role: authorization.Role{ID: 10, Name: "@everyone"}}); err != nil {
		t.Fatal(err)
	}
	query()
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked read = %+v", got)
	}
	changed := want
	changed.MaxClients = 1
	send(t, member, netproto.MsgServerConfigSet, changed)
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked save = %+v", got)
	}
	if got := env.srv.serverConfig(); got != want {
		t.Fatalf("revoked save changed runtime: %+v", got)
	}
	if got, _, err := env.chat.GetServerSetting(t.Context(), "max_clients_override"); err != nil || got != "75" {
		t.Fatalf("revoked save changed storage: %q, %v", got, err)
	}
}
