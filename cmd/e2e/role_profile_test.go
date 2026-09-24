package main

import (
	"reflect"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestE2EProfileRejectsInvalidOptionsBeforeNetwork(t *testing.T) {
	for _, o := range []options{
		{authorizationModel: "typo"},
		{authorizationModel: "roles-v1", filePayload: 1, chaos: true},
		{authorizationModel: "roles-v1", filePayload: 0},
		{authorizationModel: "roles-v1", filePayload: 16<<20 + 1},
	} {
		if got := runChecks(o); got != 2 {
			t.Fatalf("invalid options returned %d, want 2", got)
		}
	}
}

func TestRoleFileDenialRejectsTokenAfterError(t *testing.T) {
	conn := &deadlineRecordingConn{}
	queueDeadlineTestFrame(t, conn, netproto.MsgError, netproto.Error{Code: 4, OriginType: uint16(netproto.MsgFileTransferInit)})
	queueDeadlineTestFrame(t, conn, netproto.MsgFileTransferInitResponse, netproto.FileTransferInitResponse{Token: "must-not-be-issued"})
	queueDeadlineTestFrame(t, conn, netproto.MsgPong, netproto.Pong{})
	if err := readRoleFileDenial(&client{conn: conn}); err == nil {
		t.Fatal("accepted transfer token after denial")
	}
}

func roleProfilePolicy() authorization.RolePolicy {
	return authorization.RolePolicy{Revision: 1, OwnerID: 1, EveryoneID: 10, Roles: []authorization.Role{{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.SendMessages, authorization.ReadHistory}}}}
}

func TestRoleProfileBaselineRejectsUnsuitablePermissions(t *testing.T) {
	s := &roleScenario{alice: authorization.MemberIdentity{UserID: 2}, bob: authorization.MemberIdentity{UserID: 3}}
	if err := checkRoleProfileBaseline(s, roleProfilePolicy()); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []authorization.Capability{authorization.Administrator, authorization.ManageRoles, authorization.ManageChannels, authorization.ManageMessages, authorization.BypassSlowmode} {
		p := roleProfilePolicy()
		p.Roles[0].Permissions = append(p.Roles[0].Permissions, cap)
		if err := checkRoleProfileBaseline(s, p); err == nil {
			t.Fatalf("accepted baseline with %s", cap)
		}
	}
	p := roleProfilePolicy()
	p.Roles[0].Permissions = nil
	if err := checkRoleProfileBaseline(s, p); err == nil {
		t.Fatal("accepted missing public chat permissions")
	}
}

func TestRolePolicyRestorationIgnoresOrderingButDetectsChanges(t *testing.T) {
	p := roleProfilePolicy()
	q := roleProfilePolicy()
	q.Revision++
	q.Roles[0].Permissions[0], q.Roles[0].Permissions[1] = q.Roles[0].Permissions[1], q.Roles[0].Permissions[0]
	before := roleProfilePolicy()
	if err := verifyRolePolicyRestored(p, q); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("restoration check mutated its input")
	}
	q.Roles[0].Name = "changed"
	if err := verifyRolePolicyRestored(p, q); err == nil {
		t.Fatal("accepted changed policy")
	}
}

func TestRolePolicyRestorationUsesRelativeRank(t *testing.T) {
	p := roleProfilePolicy()
	p.Roles = append(p.Roles, authorization.Role{ID: 20, Name: "lower", Position: 10}, authorization.Role{ID: 30, Name: "higher", Position: 20})
	q := roleProfilePolicy()
	q.Roles = append(q.Roles, authorization.Role{ID: 30, Name: "higher", Position: 2}, authorization.Role{ID: 20, Name: "lower", Position: 1})
	if err := verifyRolePolicyRestored(p, q); err != nil {
		t.Fatalf("unchanged relative ranks rejected: %v", err)
	}
	q.Roles[1].Position, q.Roles[2].Position = 1, 2
	if err := verifyRolePolicyRestored(p, q); err == nil {
		t.Fatal("changed hierarchy accepted")
	}
}
