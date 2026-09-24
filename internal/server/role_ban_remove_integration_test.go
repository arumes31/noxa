//go:build integration

package server

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleBanRemovalPostgresAcknowledgementAndRevocation(t *testing.T) {
	db := integrationManagementStore(t)
	var first, second int64
	for i, dest := range []*int64{&first, &second} {
		if err := db.DB().QueryRowContext(t.Context(), "INSERT INTO bans (ban_type,value) VALUES (1,$1) RETURNING id", "banned-"+strconv.Itoa(i)).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.BanAdmin = db; d.Groups = db })
	defer env.stop()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	grant := func(revision int64, capabilities ...authorization.Capability) {
		t.Helper()
		_, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: revision, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: capabilities}})
		if err != nil {
			t.Fatal(err)
		}
	}
	remove := func(id int64) {
		t.Helper()
		send(t, member, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: id})
		var result netproto.RoleBanRemoved
		if err := netproto.Decode(readOfType(t, member, netproto.MsgRoleBanRemoved), &result); err != nil || result.BanID != id {
			t.Fatalf("result: %+v %v", result, err)
		}
	}
	grant(1, authorization.BanMembers)
	remove(first)
	remove(first) // An already absent ID succeeds without removing another ban.
	var remaining int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM bans WHERE id=$1", second).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("other ban: %d %v", remaining, err)
	}
	var actor, target string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT actor_unique_id,target FROM audit_log WHERE action='ban_remove' ORDER BY id DESC LIMIT 1").Scan(&actor, &target); err != nil || actor != "admin-uid" || target != strconv.FormatInt(first, 10) {
		t.Fatalf("audit: %s %s %v", actor, target, err)
	}
	grant(2)
	send(t, member, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: second})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	grant(3, authorization.BanMembers)
	if _, err := db.DB().ExecContext(t.Context(), `CREATE FUNCTION reject_ban_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private database failure'; END $$;
		CREATE TRIGGER reject_ban_removal BEFORE DELETE ON bans FOR EACH ROW EXECUTE FUNCTION reject_ban_removal()`); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: second})
	if got := readError(t, member); got.Code != errCodeUnavailable || strings.Contains(got.Message, "private") {
		t.Fatal(got)
	}
	if err := db.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM bans WHERE id=$1", second).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("failed removal: %d %v", remaining, err)
	}
}
