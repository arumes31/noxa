package server

import (
	"context"
	"testing"

	"noxa/internal/netproto"
	"noxa/internal/permissions"
	"noxa/internal/store"
)

// TestGroupAssignSelfEscalationPoC: a non-admin holding only
// b_server_group_manage attempts to assign themselves into a server group
// whose entries include b_permission_manage (full permission management).
// Pre-fix this write succeeded; post-fix it must be denied.
func TestGroupAssignSelfEscalationPoC(t *testing.T) {
	manage := boolPerm(permissions.PermissionKeyServerGroupManage, true)
	tp := tieredWith(manage)
	env := startTestEnv(t, &tp)
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()

	gid, err := env.groups.CreateGroup(context.Background(), "server", "HeadAdmin", 0)
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	// Seed the target group with b_permission_manage: membership in it yields
	// full permission management.
	if err := env.groups.SetPermission(context.Background(), store.PermTierServerGroup,
		store.PermTarget{GroupID: gid}, string(permissions.PermissionKeyPermissionManage), 1, 1, false, false); err != nil {
		t.Fatalf("seed perm: %v", err)
	}

	send(t, conn, netproto.MsgGroupAssign, netproto.GroupAssign{
		Type: "server", GroupID: gid, UniqueID: "user-uid",
	})
	if e := readError(t, conn); e.Code != errCodePermissionDenied {
		t.Fatalf("error = %+v, want permission denied (attacker must not step up into HeadAdmin)", e)
	}
	if env.groups.hasServerMember(gid, 2) {
		t.Fatal("attacker was added to the privileged group; stage-up guard failed")
	}
}
