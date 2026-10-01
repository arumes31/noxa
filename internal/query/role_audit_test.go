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

type auditQueryBackend struct {
	*roleQueryBackend
	request netproto.AuditLog
	calls   int
	err     error
}

func (b *auditQueryBackend) WithIntegrationAudit(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.AuditLog, deliver func(context.Context, netproto.AuditLogResponse) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.request = request
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.AuditLogResponse{Entries: []netproto.AuditEntry{
		{ID: 19, Actor: "canonical", Action: "role_update", Target: "role:10", Detail: "Detail | with\nnewlines", CreatedAt: 100, Structured: true},
		{ID: 18, CreatedAt: 99, Restricted: true},
	}})
}

func TestRoleAuditQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &auditQueryBackend{roleQueryBackend: &roleQueryBackend{}}
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
			request := netproto.AuditLog{BeforeID: 20, Limit: 2}
			rows := execute(inspectionCommand(t, "auditquery", request))
			var page netproto.AuditLogResponse
			if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &page); err != nil || len(page.Entries) != 2 || page.Entries[0].Detail != "Detail | with\nnewlines" || !page.Entries[0].Structured || !page.Entries[1].Restricted || page.Entries[1].ID != 18 || page.Entries[1].Actor != "" {
				t.Fatalf("audit page: %+v %v", page, err)
			}
			for _, invalid := range []string{`auditquery`, `auditquery data={}{}`, `auditquery data={"before_id":-1}`, `auditquery data={"limit":-1}`, `auditquery data={"limit":201}`, `auditquery data={"actor_id":1}`, `auditquery data={} extra=1`, `auditquery data={} extra`} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			b.mu.Lock()
			calls, got := b.calls, b.request
			b.err = auth.ErrIntegrationDenied
			b.mu.Unlock()
			if calls != 1 || got != request {
				t.Fatalf("invalid requests reached backend or cursor changed: %d %+v", calls, got)
			}
			if got := lastErr(t, execute("auditquery data={}")); !strings.Contains(got, "integration\\saccess\\sdenied") {
				t.Fatal(got)
			}
		})
	}
}
