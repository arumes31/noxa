package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type roleVoiceQueryBackend struct {
	*roleQueryBackend
	request           netproto.MemberVoiceSet
	calls             int
	moveRequest       netproto.MoveClient
	moveErr           error
	disconnectRequest netproto.MemberDisconnect
	disconnectErr     error
}

func (b *roleVoiceQueryBackend) DisconnectIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.disconnectRequest = request
	return netproto.MemberDisconnectResult{ClientID: request.ClientID, ChannelID: request.ChannelID}, b.disconnectErr
}

func TestRoleQueryMemberDisconnect(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	request := netproto.MemberDisconnect{ClientID: "target", ChannelID: 2, Reason: "A reason with spaces"}
	command := inspectionCommand(t, "memberdisconnect", request)
	rows := sendCmd(t, conn, reader, command)
	var result netproto.MemberDisconnectResult
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil || result.ClientID != "target" || result.ChannelID != 2 {
		t.Fatalf("disconnect result: %+v %v", result, err)
	}
	b.mu.Lock()
	got := b.disconnectRequest
	b.disconnectErr = authorization.ErrRoleForbidden
	b.mu.Unlock()
	if got != request {
		t.Fatalf("disconnect fields changed: %+v", got)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=2568 ") {
		t.Fatal(got)
	}
	for _, invalid := range []string{
		`memberdisconnect data={"client_id":"target","channel_id":0}`,
		`memberdisconnect data={"client_id":"target","channel_id":2,"from_server":true}`,
		`memberdisconnect data={"client_id":"target","channel_id":2,"ban":true}`,
	} {
		if got := lastErr(t, sendCmd(t, conn, reader, invalid)); !strings.HasPrefix(got, "error id=512 ") {
			t.Fatal(got)
		}
	}
}

func TestRoleSSHMemberDisconnect(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
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
	request := netproto.MemberDisconnect{ClientID: "target", ChannelID: 2, Reason: "Take a break"}
	out, err := session.Output(inspectionCommand(t, "memberdisconnect", request))
	if err != nil || !strings.Contains(string(out), `"channel_id":2`) {
		t.Fatalf("disconnect response: %s %v", out, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disconnectRequest != request {
		t.Fatalf("SSH disconnect changed request: %+v", b.disconnectRequest)
	}
}

func (b *roleVoiceQueryBackend) MoveIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MoveClient) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.moveRequest = request
	return b.moveErr
}

func TestRoleQueryMemberMove(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	request := netproto.MoveClient{ClientID: "target", ChannelID: 2}
	command := inspectionCommand(t, "membermove", request)
	rows := sendCmd(t, conn, reader, command)
	var result netproto.MoveClient
	if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil || result != request {
		t.Fatalf("committed move response: %+v %v", result, err)
	}
	b.mu.Lock()
	got := b.moveRequest
	b.moveErr = authorization.ErrRoleConflict
	b.mu.Unlock()
	if got != request {
		t.Fatalf("wrong destination request: %+v", got)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=521 ") {
		t.Fatal(got)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, `membermove data={"client_id":"target","channel_id":0}`)); !strings.HasPrefix(got, "error id=512 ") {
		t.Fatal(got)
	}
	b.mu.Lock()
	b.moveErr = errors.Join(auth.ErrIntegrationDenied, errors.New("private account detail"))
	b.mu.Unlock()
	if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=2568 ") || strings.Contains(got, "private") {
		t.Fatal(got)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=2568 ") {
		t.Fatal(got)
	}
}

func TestRoleSSHMemberMove(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
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
	request := netproto.MoveClient{ClientID: "target", ChannelID: 2}
	out, err := session.Output(inspectionCommand(t, "membermove", request))
	if err != nil || !strings.Contains(string(out), `"channel_id":2`) {
		t.Fatalf("move response: %s %v", out, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.moveRequest != request {
		t.Fatalf("SSH movement changed request: %+v", b.moveRequest)
	}
}

func (b *roleVoiceQueryBackend) SetIntegrationMemberVoice(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.request, b.calls = request, b.calls+1
	return netproto.MemberVoiceState{Revision: 12, ClientID: request.ClientID, ChannelID: request.ChannelID, Muted: request.Muted != nil && *request.Muted, Deafened: request.Deafened != nil && *request.Deafened}, nil
}

func TestRoleQueryMemberVoiceFlags(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	on, off := true, false
	for _, request := range []netproto.MemberVoiceSet{
		{ClientID: "target", ChannelID: 2, Muted: &on},
		{ClientID: "target", ChannelID: 2, Muted: &off},
		{ClientID: "target", ChannelID: 2, Deafened: &on},
		{ClientID: "target", ChannelID: 2, Muted: &on, Deafened: &off},
	} {
		rows := sendCmd(t, conn, reader, inspectionCommand(t, "membervoice", request))
		var response netproto.MemberVoiceState
		if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &response); err != nil {
			t.Fatal(err)
		}
		if response.Revision != 12 || response.ClientID != "target" || response.ChannelID != 2 {
			t.Fatalf("voice acknowledgement: %+v", response)
		}
		b.mu.Lock()
		got := b.request
		b.mu.Unlock()
		if !reflect.DeepEqual(got, request) {
			t.Fatalf("flag presence changed: %+v", got)
		}
	}
	for _, command := range []string{
		`membervoice data={"client_id":"target","channel_id":2}`,
		`membervoice data={"client_id":"target","channel_id":0,"muted":true}`,
		`membervoice data={"client_id":"target","channel_id":2,"muted":true,"actor_id":1}`,
		"membervoice data=" + escape(`{"client_id":"target","channel_id":2,"muted":true} {}`),
	} {
		if got := lastErr(t, sendCmd(t, conn, reader, command)); !strings.HasPrefix(got, "error id=512 ") {
			t.Fatal(got)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls != 4 {
		t.Fatal("invalid voice request reached backend")
	}
}

func TestRoleSSHMemberVoice(t *testing.T) {
	b := &roleVoiceQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
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
	off := false
	out, err := session.Output(inspectionCommand(t, "membervoice", netproto.MemberVoiceSet{ClientID: "target", ChannelID: 2, Muted: &off}))
	if err != nil || !strings.Contains(string(out), `"revision":12`) {
		t.Fatalf("voice reply: %s %v", out, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.request.Muted == nil || *b.request.Muted || b.request.Deafened != nil {
		t.Fatal("SSH lost explicit false or absence")
	}
}
