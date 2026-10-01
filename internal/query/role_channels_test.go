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

type roleChannelQueryBackend struct {
	*roleQueryBackend
	request netproto.RoleChannelChange
	query   netproto.RoleChannelQuery
	changes int
	err     error
}

func (b *roleChannelQueryBackend) WithIntegrationChannelState(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.RoleChannelQuery, deliver func(context.Context, netproto.RoleChannelState) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.query = request
	return deliver(ctx, netproto.RoleChannelState{Revision: 7, ChannelID: request.ChannelID, CanCreateTemporary: true})
}

func (b *roleChannelQueryBackend) ChangeIntegrationChannel(_ context.Context, _ auth.IntegrationPrincipal, request netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.request, b.changes = request, b.changes+1
	return netproto.RoleChannelResult{Revision: 8, ChannelID: 12, EnforcementPending: true}, b.err
}

func TestRoleQueryChannelLifecycle(t *testing.T) {
	b := &roleChannelQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	request := netproto.RoleChannelQuery{Kind: authorization.ChannelCreate, ChannelID: 2}
	rows := sendCmd(t, conn, reader, inspectionCommand(t, "channelquery", request))
	var state netproto.RoleChannelState
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &state); err != nil {
		t.Fatal(err)
	}
	if state.Revision != 7 || state.ChannelID != 2 || !state.CanCreateTemporary {
		t.Fatalf("state: %+v", state)
	}
	for _, change := range []netproto.RoleChannelChange{
		{Kind: authorization.ChannelCreate, ExpectedRevision: 7, ParentID: 2, ChannelType: 2, Password: "secret with spaces", Settings: &netproto.RoleChannelSettings{Name: "Channel with spaces", OpusBitrate: 64000}, Access: &netproto.RoleChannelAccess{Synced: false, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Allow}}}},
		{Kind: authorization.ChannelEdit, ExpectedRevision: 7, ChannelID: 12, Settings: &netproto.RoleChannelSettings{Name: "Edited"}},
		{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 12, ParentID: 3, SyncToParent: true},
		{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 12, ParentID: 3, OrderIndex: new(int32)},
		{Kind: authorization.ChannelDelete, ExpectedRevision: 7, ChannelID: 12},
	} {
		rows = sendCmd(t, conn, reader, inspectionCommand(t, "channelchange", change))
		var result netproto.RoleChannelResult
		if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil {
			t.Fatal(err)
		}
		if result.Revision != 8 || result.ChannelID != 12 || !result.EnforcementPending {
			t.Fatalf("commit: %+v", result)
		}
		b.mu.Lock()
		got, query := b.request, b.query
		b.mu.Unlock()
		if !reflect.DeepEqual(change, got) || query != request {
			t.Fatal("channel request fields changed")
		}
	}
	for _, command := range []string{
		`channelchange data={"kind":"channel_delete","expected_revision":7,"actor_id":1}`,
		`channelchange data={"kind":"channel_delete"}`,
		"channelchange data=" + escape(`{"expected_revision":7} {}`),
		`channelquery data={"kind":"channel_edit","channel_id":-1}`,
	} {
		if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=512 ") {
			t.Fatal(got)
		}
	}
	b.mu.Lock()
	count := b.changes
	b.err = authorization.ErrRoleConflict
	b.mu.Unlock()
	if count != 5 {
		t.Fatal("invalid channel request reached backend")
	}
	if got := lastErr(t, sendCmd(t, conn, reader, inspectionCommand(t, "channelchange", netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ExpectedRevision: 7, ChannelID: 12}))); !strings.HasPrefix(got, "error id=521 ") {
		t.Fatal(got)
	}
}

func TestRoleSSHChannelLifecycle(t *testing.T) {
	b := &roleChannelQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	move := netproto.RoleChannelChange{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 12, ParentID: 3, OrderIndex: new(int32)}
	for _, command := range []string{
		inspectionCommand(t, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelCreate}),
		inspectionCommand(t, "channelchange", netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ExpectedRevision: 7, ChannelID: 12}),
		inspectionCommand(t, "channelchange", move),
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
		if err != nil || !strings.Contains(string(out), "error id=0 msg=ok") {
			t.Fatalf("channel command: %s %v", out, err)
		}
	}
	b.mu.Lock()
	got := b.request
	b.mu.Unlock()
	if !reflect.DeepEqual(got, move) {
		t.Fatalf("move fields changed: %+v", got)
	}
}
