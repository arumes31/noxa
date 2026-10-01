package query

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type roleManagementQueryBackend struct {
	*roleQueryBackend
	change      authorization.RoleChange
	changes     int
	channelID   int64
	changeError error
	onChange    func()
}

func (b *roleManagementQueryBackend) WithIntegrationRoleState(ctx context.Context, p auth.IntegrationPrincipal, channelID int64, deliver func(context.Context, netproto.RoleState) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.channelID = channelID
	return deliver(ctx, netproto.RoleState{Policy: authorization.RolePolicy{Revision: 7, Roles: []authorization.Role{{ID: 10, Name: "@everyone"}}}})
}

func (b *roleManagementQueryBackend) ChangeIntegrationRoles(_ context.Context, _ auth.IntegrationPrincipal, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.change, b.changes = change, b.changes+1
	if b.onChange != nil {
		b.onChange()
	}
	return netproto.RoleChangeResult{Revision: 8, CreatedRoleID: 50, EnforcementPending: true}, b.changeError
}

func roleChangeCommand(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: 7, Role: authorization.Role{Name: "Name with spaces"}})
	if err != nil {
		t.Fatal(err)
	}
	return "rolechange data=" + escape(string(data))
}

func TestRoleQueryManagementProtocol(t *testing.T) {
	b := &roleManagementQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if help := strings.Join(sendCmd(t, conn, reader, "help"), "\n"); !strings.Contains(help, "rolechange") || strings.Contains(help, "servergroupadd") {
		t.Fatalf("role help advertised the wrong commands: %s", help)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	rows := sendCmd(t, conn, reader, "rolelist cid=2")
	var state netproto.RoleState
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &state); err != nil {
		t.Fatal(err)
	}
	if state.Policy.Revision != 7 {
		t.Fatalf("state: %+v", state)
	}
	b.mu.Lock()
	channelID := b.channelID
	b.mu.Unlock()
	if channelID != 2 {
		t.Fatalf("scope dropped: %d", channelID)
	}
	rows = sendCmd(t, conn, reader, roleChangeCommand(t))
	var result netproto.RoleChangeResult
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 8 || !result.EnforcementPending || result.CreatedRoleID != 50 {
		t.Fatalf("commit result: %+v", result)
	}
	b.mu.Lock()
	change, count := b.change, b.changes
	b.mu.Unlock()
	if change.Role.Name != "Name with spaces" || change.ExpectedRevision != 7 || count != 1 {
		t.Fatalf("change dropped fields: %+v count=%d", change, count)
	}
	for _, data := range []string{
		`{"kind":"role_create","expected_revision":7,"actor_id":1}`,
		`{"kind":"role_create"}`,
		`{"kind":"role_create","expected_revision":7} {}`,
	} {
		if got := lastErr(t, sendCmd(t, conn, reader, "rolechange data="+escape(data))); !strings.HasPrefix(got, "error id=512 ") {
			t.Fatal(got)
		}
	}
	b.mu.Lock()
	count = b.changes
	b.changeError = authorization.ErrRoleConflict
	b.mu.Unlock()
	if count != 1 {
		t.Fatal("invalid request reached mutation")
	}
	if got := lastErr(t, sendCmd(t, conn, reader, roleChangeCommand(t))); !strings.HasPrefix(got, "error id=521 ") {
		t.Fatal(got)
	}
}

func TestRoleQueryAcknowledgesCommitAfterMutationContextExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &roleManagementQueryBackend{roleQueryBackend: &roleQueryBackend{}, onChange: cancel}
	s := New("", nil, b)
	var out bytes.Buffer
	sess := &session{authed: true, authorizationModel: "roles-v1", w: &out, setWriteDeadline: func(time.Time) error { return nil }, closeTransport: func() error { return nil }}
	if !s.execute(ctx, sess, roleChangeCommand(t)) {
		t.Fatal("known commit lost its acknowledgement")
	}
	if ctx.Err() == nil || !strings.Contains(out.String(), `"revision":8`) || !strings.Contains(out.String(), `"enforcement_pending":true`) {
		t.Fatalf("reply after mutation expiry: %s", out.String())
	}
}

func TestRoleSSHManagementProtocol(t *testing.T) {
	b := &roleManagementQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr := startSSHQuery(t, b)
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, session)
	if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
		t.Fatal(err)
	}
	out, err := session.Output(roleChangeCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"revision":8`) {
		t.Fatalf("SSH change lost acknowledgement: %s", out)
	}
}
