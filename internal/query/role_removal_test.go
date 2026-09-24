package query

import (
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

type removalQueryBackend struct {
	*roleQueryBackend
	kick        netproto.MemberKick
	ban         netproto.MemberBan
	persistence netproto.BanPersistence
	denied      bool
	calls       int
}

func (b *removalQueryBackend) KickIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberKick) (netproto.MemberKickResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.denied {
		return netproto.MemberKickResult{}, authorization.ErrRoleForbidden
	}
	b.kick = request
	return netproto.MemberKickResult{ClientID: request.ClientID, CleanupPending: true}, nil
}

func (b *removalQueryBackend) BanIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberBan) (netproto.MemberBanResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.denied {
		return netproto.MemberBanResult{}, auth.ErrIntegrationDenied
	}
	b.ban = request
	return netproto.MemberBanResult{UniqueID: "canonical-member", Persistence: b.persistence, CleanupPending: true}, nil
}

func TestRoleRemovalQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &removalQueryBackend{roleQueryBackend: &roleQueryBackend{}, persistence: netproto.BanSaved}
			var execute func(string) []string
			if transport == "query" {
				addr, _ := startQueryServer(t, b)
				conn, reader := dialQuery(t, addr)
				t.Cleanup(func() { closeServerQueryTestResource(t, conn) })
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				execute = func(cmd string) []string { return sendCmd(t, conn, reader, cmd) }
				if got := lastErr(t, execute("login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
					t.Fatal(got)
				}
			} else {
				client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeServerQueryTestResource(t, client) })
				execute = func(cmd string) []string {
					session, err := client.NewSession()
					if err != nil {
						t.Fatal(err)
					}
					defer closeServerQueryTestResource(t, session)
					if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
						t.Fatal(err)
					}
					out, err := session.Output(cmd)
					if err != nil {
						t.Fatal(err)
					}
					return strings.Split(strings.TrimSpace(string(out)), "\n")
				}
			}
			kick := netproto.MemberKick{ClientID: "target", Reason: "A reason with spaces | and \\ separators"}
			rows := execute(inspectionCommand(t, "memberkick", kick))
			var kicked netproto.MemberKickResult
			if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &kicked); err != nil || kicked.ClientID != kick.ClientID || !kicked.CleanupPending {
				t.Fatalf("kick result: %+v %v", kicked, err)
			}
			ban := netproto.MemberBan{ClientID: "target", DurationSeconds: 60, Reason: kick.Reason}
			for _, persistence := range []netproto.BanPersistence{netproto.BanSaved, netproto.BanUnconfirmed} {
				b.mu.Lock()
				b.persistence = persistence
				b.mu.Unlock()
				rows = execute(inspectionCommand(t, "memberban", ban))
				var banned netproto.MemberBanResult
				if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &banned); err != nil || banned.Persistence != persistence || !banned.CleanupPending || banned.UniqueID != "canonical-member" {
					t.Fatalf("ban outcome: %+v %v", banned, err)
				}
			}
			for _, invalid := range []string{
				`memberkick data={"client_id":"target","actor_id":1}`,
				`memberban data={"client_id":"target","duration_seconds":-1}`,
				`memberban data={"client_id":"target","duration_seconds":9223372037}`,
				`memberban data={"client_id":"target","ban":false}`,
			} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			b.mu.Lock()
			calls, gotKick, gotBan := b.calls, b.kick, b.ban
			b.denied = true
			b.mu.Unlock()
			if calls != 3 || gotKick != kick || gotBan != ban {
				t.Fatalf("invalid request reached backend or fields changed: %d %+v %+v", calls, gotKick, gotBan)
			}
			for _, command := range []string{inspectionCommand(t, "memberkick", kick), inspectionCommand(t, "memberban", ban)} {
				if got := lastErr(t, execute(command)); !strings.HasPrefix(got, "error id=2568 ") {
					t.Fatal(got)
				}
			}
		})
	}
}
