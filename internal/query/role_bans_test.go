package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type banQueryBackend struct {
	*roleQueryBackend
	request netproto.BanQuery
	err     error
	calls   int
}

func (b *banQueryBackend) WithIntegrationBans(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.BanQuery, deliver func(context.Context, netproto.BanPage) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return b.err
	}
	b.request = request
	return deliver(ctx, netproto.BanPage{Bans: []netproto.BanEntry{{ID: 9, Value: "canonical-uid", Reason: "Reason | with \\ and spaces", CreatedAt: 100, ExpiresAt: 200}}, NextBeforeID: 9})
}

func TestRoleBanQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &banQueryBackend{roleQueryBackend: &roleQueryBackend{}}
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
			request := netproto.BanQuery{BeforeID: 20, Limit: 1}
			rows := execute(inspectionCommand(t, "banquery", request))
			var page netproto.BanPage
			if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &page); err != nil || len(page.Bans) != 1 || page.NextBeforeID != 9 || page.Bans[0].Reason != "Reason | with \\ and spaces" || page.Bans[0].ExpiresAt != 200 {
				t.Fatalf("ban page: %+v %v", page, err)
			}
			for _, invalid := range []string{
				`banquery data={"before_id":-1}`, `banquery data={"limit":101}`,
				`banquery data={"limit":-1}`, `banquery data={"actor_id":1}`,
				`banquery`, `banquery data={}{}`,
			} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			b.mu.Lock()
			calls, got := b.calls, b.request
			b.mu.Unlock()
			if calls != 1 || got != request {
				t.Fatalf("invalid requests reached backend or cursor changed: %d %+v", calls, got)
			}
			for _, err := range []error{authorization.ErrRoleForbidden, errors.New("private database detail"), auth.ErrIntegrationDenied} {
				b.mu.Lock()
				b.err = err
				b.mu.Unlock()
				rows = execute("banquery data={}")
				if len(rows) != 1 || strings.Contains(rows[0], "private") || strings.HasPrefix(rows[0], "error id=0 ") {
					t.Fatalf("denied response: %v", rows)
				}
			}
		})
	}
}
