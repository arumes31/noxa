package query

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type rulesQueryBackend struct {
	*roleQueryBackend
	calls  int
	denied bool
}

func (b *rulesQueryBackend) WithIntegrationRules(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.RulesInspection) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.denied {
		return auth.ErrIntegrationDenied
	}
	return deliver(ctx, netproto.RulesInspection{Text: "Rules | with\nnewlines", Hash: "wording-hash", AcceptedClients: 17})
}

func TestRoleRulesQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &rulesQueryBackend{roleQueryBackend: &roleQueryBackend{}}
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
			rows := execute("rulesquery")
			var result netproto.RulesInspection
			if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil || result.Text != "Rules | with\nnewlines" || result.Hash != "wording-hash" || result.AcceptedClients != 17 {
				t.Fatalf("rules: %+v %v", result, err)
			}
			for _, invalid := range []string{`rulesquery data={}`, `rulesquery actor_id=1`, `rulesquery extra`} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			b.mu.Lock()
			calls := b.calls
			b.denied = true
			b.mu.Unlock()
			if calls != 1 {
				t.Fatal("invalid input reached backend")
			}
			if got := lastErr(t, execute("rulesquery")); !strings.Contains(got, "integration\\saccess\\sdenied") {
				t.Fatal(got)
			}
		})
	}
}
