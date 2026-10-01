package query

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type roleInspectionQueryBackend struct {
	*roleQueryBackend
	members authorization.MemberQuery
	check   netproto.AccessCheck
	calls   int
	err     error
}

func (b *roleInspectionQueryBackend) WithIntegrationRoleMembers(ctx context.Context, _ auth.IntegrationPrincipal, request authorization.MemberQuery, deliver func(context.Context, authorization.MemberPage) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.members, b.calls = request, b.calls+1
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, authorization.MemberPage{Revision: 7, Entries: []authorization.MemberIdentity{{UserID: 99, Nickname: "Protected owner", Manageable: false}}, More: true})
}

func (b *roleInspectionQueryBackend) WithIntegrationAccessCheck(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.AccessCheck, deliver func(context.Context, netproto.AccessCheckResult) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.check, b.calls = request, b.calls+1
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.AccessCheckResult{Decision: authorization.RoleDecision{Revision: 7, Allowed: true, Reason: "owner"}, CanManageMember: false})
}

func inspectionCommand(t *testing.T, command string, request any) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return command + " data=" + escape(string(data))
}

func TestRoleQueryInspectionProtocol(t *testing.T) {
	b := &roleInspectionQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	members := authorization.MemberQuery{ChannelID: 2, ExpectedRevision: 7, Search: "Name with spaces", AfterID: 8}
	rows := sendCmd(t, conn, reader, inspectionCommand(t, "rolemembers", members))
	var page authorization.MemberPage
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &page); err != nil {
		t.Fatal(err)
	}
	if page.Revision != 7 || !page.More || len(page.Entries) != 1 || page.Entries[0].Manageable {
		t.Fatalf("page: %+v", page)
	}
	check := netproto.AccessCheck{ChannelID: 2, UserID: 99, ExpectedRevision: 7, Capability: authorization.Speak}
	rows = sendCmd(t, conn, reader, inspectionCommand(t, "accesscheck", check))
	var result netproto.AccessCheckResult
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil {
		t.Fatal(err)
	}
	if result.Decision.Revision != 7 || !result.Decision.Allowed || result.CanManageMember {
		t.Fatalf("check: %+v", result)
	}
	b.mu.Lock()
	gotMembers, gotCheck := b.members, b.check
	b.mu.Unlock()
	if !reflect.DeepEqual(members, gotMembers) || check != gotCheck {
		t.Fatalf("requests changed: %+v %+v", gotMembers, gotCheck)
	}
	for _, command := range []string{
		`rolemembers data={"expected_revision":7,"actor_id":1}`,
		`rolemembers data={"expected_revision":7,"after_id":-1}`,
		"rolemembers data=" + escape(`{"expected_revision":7} {}`),
		`accesscheck data={"expected_revision":7,"capability":"speak","user_id":-1}`,
		`accesscheck data={"expected_revision":0,"capability":"speak"}`,
		`accesscheck data={"expected_revision":7}`,
	} {
		if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=512 ") {
			t.Fatalf("%s: %s", command, got)
		}
	}
	b.mu.Lock()
	calls := b.calls
	b.err = authorization.ErrRoleConflict
	b.mu.Unlock()
	if calls != 2 {
		t.Fatal("invalid request reached backend")
	}
	if got := lastErr(t, sendCmd(t, conn, reader, inspectionCommand(t, "accesscheck", check))); !strings.HasPrefix(got, "error id=521 ") {
		t.Fatal(got)
	}
	b.mu.Lock()
	b.err = auth.ErrIntegrationDenied
	b.mu.Unlock()
	if got := lastErr(t, sendCmd(t, conn, reader, inspectionCommand(t, "rolemembers", members))); !strings.HasPrefix(got, "error id=2568 ") {
		t.Fatal(got)
	}
	b.mu.Lock()
	calls = b.calls
	b.mu.Unlock()
	sendCmd(t, conn, reader, inspectionCommand(t, "accesscheck", check))
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls != calls {
		t.Fatal("denied identity retained authenticated session")
	}
}

func TestRoleSSHInspectionProtocol(t *testing.T) {
	b := &roleInspectionQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	for _, command := range []string{
		inspectionCommand(t, "rolemembers", authorization.MemberQuery{ExpectedRevision: 7}),
		inspectionCommand(t, "accesscheck", netproto.AccessCheck{ExpectedRevision: 7, UserID: 99, Capability: authorization.Speak}),
	} {
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
			t.Fatal(err)
		}
		out, err := session.Output(command)
		closeServerQueryTestResource(t, session)
		if err != nil || !strings.Contains(string(out), `"revision":7`) {
			t.Fatalf("%s: %s %v", command, out, err)
		}
	}
}
