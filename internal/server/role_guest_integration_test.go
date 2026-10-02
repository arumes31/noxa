//go:build integration

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type staleGuestLookup struct {
	*auth.AuthService
	staleID, staleKey atomic.Bool
}

func (a *staleGuestLookup) LookupUser(ctx context.Context, uid string) (*auth.User, error) {
	if a.staleID.Swap(false) {
		return nil, auth.ErrUserNotFound
	}
	return a.AuthService.LookupUser(ctx, uid)
}

func (a *staleGuestLookup) LookupUserByPublicKey(ctx context.Context, key string) (*auth.User, error) {
	if a.staleKey.Swap(false) {
		return nil, auth.ErrUserNotFound
	}
	return a.AuthService.LookupUserByPublicKey(ctx, key)
}

func TestGuestRoleAssignmentEnrolsOnlyTheSelectedIdentity(t *testing.T) {
	db := integrationManagementStore(t)
	a := &staleGuestLookup{AuthService: auth.New(db, zap.NewNop())}
	ownerUID, err := a.RegisterUser(t.Context(), "owner", "test-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.LookupUser(t.Context(), ownerUID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := db.PrepareRolePolicy(t.Context(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var memberRole int64
	for _, role := range policy.Roles {
		if role.Name == "Member" {
			memberRole = role.ID
		}
	}
	if memberRole == 0 {
		t.Fatal("missing Member role")
	}
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.Auth, d.Roles = authority, a, db })
	defer env.stop()
	public, private, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := auth.UniqueIDFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	guest, response := dialNamedIdentity(t, env.addr, uid, public, private, "becksn", true)
	defer func() { _ = guest.Close() }()
	assertNotEnrolled := func() {
		t.Helper()
		if _, err := a.LookupUser(t.Context(), uid); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("guest enrolled without an authorized grant: %v", err)
		}
	}
	assertNotEnrolled()
	change := authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: policy.Revision, MemberUniqueID: uid, RoleIDs: []int64{memberRole}}
	send(t, guest, netproto.MsgRoleChange, change)
	if got := readError(t, guest); got.Code != errCodePermissionDenied {
		t.Fatalf("guest could assign roles: %+v", got)
	}
	assertNotEnrolled()
	// An invalid/stale role operation must roll back the new identity as well.
	invalid := change
	invalid.RoleIDs = []int64{999999}
	if _, _, err := db.AssignGuestRoles(t.Context(), owner.ID, uid, public, "becksn", invalid); err == nil {
		t.Fatal("invalid role accepted")
	}
	assertNotEnrolled()
	invalid = change
	invalid.ExpectedRevision++
	if _, _, err := db.AssignGuestRoles(t.Context(), owner.ID, uid, public, "becksn", invalid); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale enrollment: %v", err)
	}
	assertNotEnrolled()
	ownerConn := dialRetry(t, env.addr)
	defer func() { _ = ownerConn.Close() }()
	send(t, ownerConn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "owner", Password: "test-owner-password", Nickname: "Daniel"})
	readOfType(t, ownerConn, netproto.MsgAuthResponse)
	readOfType(t, ownerConn, netproto.MsgSnapshot)
	send(t, ownerConn, netproto.MsgRoleMemberQuery, authorization.MemberQuery{ExpectedRevision: policy.Revision, Search: uid})
	var roster authorization.MemberPage
	if err := netproto.Decode(readOfType(t, ownerConn, netproto.MsgRoleMembers), &roster); err != nil {
		t.Fatal(err)
	}
	if len(roster.Entries) != 1 || roster.Entries[0].UserID != 0 || !roster.Entries[0].Manageable {
		t.Fatalf("guest missing from context lookup: %+v", roster)
	}
	assertNotEnrolled()
	send(t, ownerConn, netproto.MsgRoleChange, change)
	var ack netproto.RoleChangeResult
	if err := netproto.Decode(readOfType(t, ownerConn, netproto.MsgRoleChangeResult), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Revision != policy.Revision+1 || ack.EnforcementPending {
		t.Fatalf("assignment not applied: %+v", ack)
	}
	member, err := a.LookupUser(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := env.state.GetClient(response.ClientID)
	session, _ := env.srv.clientByID(response.ClientID)
	if member.ID <= 0 || live.UserID != member.ID || session.userID() != member.ID {
		t.Fatalf("live guest retained old authority: member=%+v live=%+v", member, live)
	}
	page, err := db.RoleMembers(t.Context(), owner.ID, authorization.MemberQuery{ExpectedRevision: ack.Revision, Search: "becksn"})
	if err != nil || len(page.Entries) != 1 || len(page.Entries[0].RoleIDs) != 1 || page.Entries[0].RoleIDs[0] != memberRole {
		t.Fatalf("persisted role: %+v %v", page, err)
	}
	if user, err := a.AuthenticateIdentifier(t.Context(), uid, "test-owner-password"); err != nil || user != nil {
		t.Fatalf("enrollment enabled password login: %+v %v", user, err)
	}
	// Simulate lookup results obtained just before the assignment committed.
	a.staleID.Store(true)
	a.staleKey.Store(true)
	reconnected, again := dialNamedIdentity(t, env.addr, uid, public, private, "becksn again", true)
	defer func() { _ = reconnected.Close() }()
	resumed, _ := env.state.GetClient(again.ClientID)
	if resumed.UserID != member.ID || again.Nickname != "becksn again" {
		t.Fatalf("reconnect lost identity/alias: %+v", resumed)
	}
}
