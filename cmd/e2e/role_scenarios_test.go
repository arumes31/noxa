package main

import (
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func queueRoleQueryResult(t *testing.T, conn *deadlineRecordingConn, result any) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	conn.input.WriteString("data=" + escapeE2EQuery(string(data)) + "\nerror id=0 msg=ok\n")
}

func TestRoleScenarioPreflightRejectsUnsafeIdentitiesBeforeWrites(t *testing.T) {
	for _, kind := range []string{"same identifiers", "not owner", "missing member", "ambiguous member", "stale member", "owner target", "assigned target"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			q := &querySession{conn: conn, r: &lineReader{conn: conn}}
			state := netproto.RoleState{ActorID: 1, Policy: authorization.RolePolicy{Revision: 7, OwnerID: 1, EveryoneID: 10, Roles: []authorization.Role{{ID: 10, Name: "@everyone"}}}}
			if kind == "not owner" {
				state.ActorID = 2
			}
			queueRoleQueryResult(t, conn, state)
			page := authorization.MemberPage{Revision: 7, Entries: []authorization.MemberIdentity{{UserID: 2, UniqueID: "alice", Manageable: true}}}
			switch kind {
			case "missing member":
				page.Entries[0].UniqueID = "someone-else"
			case "ambiguous member":
				page.More = true
			case "stale member":
				page.Revision = 6
			case "owner target":
				page.Entries[0].UserID = 1
			case "assigned target":
				page.Entries[0].RoleIDs = []int64{20}
			}
			queueRoleQueryResult(t, conn, page)
			bob := "bob"
			if kind == "same identifiers" {
				bob = "alice"
			}
			if _, err := prepareRoleScenario(q, "alice", bob); err == nil {
				t.Fatal("unsafe fixture accepted")
			}
			if strings.Contains(conn.output.String(), "rolechange") || strings.Contains(conn.output.String(), "channelchange") {
				t.Fatal("preflight mutated server")
			}
		})
	}
}

func TestRoleScenarioStopsAfterUncertainOrPendingCommit(t *testing.T) {
	for _, kind := range []string{"pending", "wrong revision", "missing role id", "existing role id", "transport failure"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			q := &querySession{conn: conn, r: &lineReader{conn: conn}}
			s := &roleScenario{query: q, revision: 7, roleIDs: map[int64]bool{10: true}}
			result := netproto.RoleChangeResult{Revision: 8, CreatedRoleID: 50}
			switch kind {
			case "pending":
				result.EnforcementPending = true
			case "wrong revision":
				result.Revision = 9
			case "missing role id":
				result.CreatedRoleID = 0
			case "existing role id":
				result.CreatedRoleID = 10
			}
			if kind != "transport failure" {
				queueRoleQueryResult(t, conn, result)
			}
			if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleCreate, Role: authorization.Role{Name: "test"}}); err == nil {
				t.Fatal("bad commit accepted")
			}
			before := conn.output.String()
			if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleDelete, RoleID: 50}); err == nil {
				t.Fatal("continued after unknown or pending state")
			}
			if conn.output.String() != before {
				t.Fatal("retried a write after uncertain or pending commit")
			}
		})
	}
}

func TestRoleScenarioStopsAfterInvalidChannelCommit(t *testing.T) {
	for _, kind := range []string{"pending", "wrong revision", "missing channel id", "existing channel id", "transport failure"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			q := &querySession{conn: conn, r: &lineReader{conn: conn}}
			s := &roleScenario{query: q, revision: 7, channelIDs: map[int64]bool{10: true}}
			result := netproto.RoleChannelResult{Revision: 8, ChannelID: 50}
			switch kind {
			case "pending":
				result.EnforcementPending = true
			case "wrong revision":
				result.Revision = 9
			case "missing channel id":
				result.ChannelID = 0
			case "existing channel id":
				result.ChannelID = 10
			}
			if kind != "transport failure" {
				queueRoleQueryResult(t, conn, result)
			}
			if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelCreate}); err == nil {
				t.Fatal("bad channel commit accepted")
			}
			before := conn.output.String()
			if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ChannelID: 50}); err == nil {
				t.Fatal("continued after invalid channel commit")
			}
			if conn.output.String() != before {
				t.Fatal("retried after invalid channel commit")
			}
		})
	}
}

func TestRoleScenarioDoesNotDeleteUnverifiedCreate(t *testing.T) {
	for _, kind := range []string{"role", "channel"} {
		t.Run(kind, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			q := &querySession{conn: conn, r: &lineReader{conn: conn}}
			s := &roleScenario{query: q, revision: 7, ownerID: 1}
			if kind == "role" {
				queueRoleQueryResult(t, conn, netproto.RoleChangeResult{Revision: 8, CreatedRoleID: 50})
				queueRoleQueryResult(t, conn, netproto.RoleState{ActorID: 1, Policy: authorization.RolePolicy{Revision: 8, OwnerID: 1, Roles: []authorization.Role{{ID: 50, Name: "unrelated"}}}})
				if err := checkRoleLifecycle(s); err == nil {
					t.Fatal("unverified role accepted")
				}
			} else {
				queueRoleQueryResult(t, conn, netproto.RoleState{ActorID: 1, Policy: authorization.RolePolicy{Revision: 7, OwnerID: 1, EveryoneID: 10}})
				queueRoleQueryResult(t, conn, netproto.RoleChannelState{Revision: 7, EveryoneID: 10, CanCreatePermanent: true, CanManageAccess: true})
				queueRoleQueryResult(t, conn, netproto.RoleChannelResult{Revision: 8, ChannelID: 50})
				queueRoleQueryResult(t, conn, netproto.RoleChannelState{Revision: 8, ChannelID: 50, Settings: netproto.RoleChannelSettings{Name: "unrelated"}})
				if err := checkRoleChannelLifecycle(s); err == nil {
					t.Fatal("unverified channel accepted")
				}
			}
			if strings.Contains(conn.output.String(), "role_delete") || strings.Contains(conn.output.String(), "channel_delete") {
				t.Fatal("cleanup targeted an unverified resource")
			}
		})
	}
}
