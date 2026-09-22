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

type complaintQueryBackend struct {
	*roleQueryBackend
	request       netproto.ComplaintQuery
	clear         netproto.ComplaintClear
	reads, clears int
	err           error
}

func (b *complaintQueryBackend) WithIntegrationComplaints(ctx context.Context, _ auth.IntegrationPrincipal, q netproto.ComplaintQuery, deliver func(context.Context, netproto.ComplaintPage) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads++
	b.request = q
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.ComplaintPage{Entries: []netproto.ComplaintPageEntry{{ID: 11, ComplaintEntry: netproto.ComplaintEntry{TargetUniqueID: "target", TargetNickname: "Target", FromUniqueID: "reporter", FromNickname: "Reporter", Reason: "Reason | with\nnewlines", CreatedAt: 100}}}, NextAfterID: 11})
}

func (b *complaintQueryBackend) ClearIntegrationComplaints(_ context.Context, _ auth.IntegrationPrincipal, q netproto.ComplaintClear) (netproto.ComplaintClearResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clears++
	b.clear = q
	if b.err != nil {
		return netproto.ComplaintClearResult{}, b.err
	}
	if b.clears == 1 {
		return netproto.ComplaintClearResult{Deleted: 2}, nil
	}
	return netproto.ComplaintClearResult{}, nil
}

func TestRoleComplaintsQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &complaintQueryBackend{roleQueryBackend: &roleQueryBackend{}}
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
			q := netproto.ComplaintQuery{AfterID: 10, Limit: 1}
			rows := execute(inspectionCommand(t, "complaintquery", q))
			var page netproto.ComplaintPage
			if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &page); err != nil || len(page.Entries) != 1 || page.NextAfterID != 11 {
				t.Fatalf("page: %+v %v", page, err)
			}
			e := page.Entries[0]
			if e.ID != 11 || e.TargetNickname != "Target" || e.FromNickname != "Reporter" || e.TargetUniqueID != "target" || e.FromUniqueID != "reporter" || e.Reason != "Reason | with\nnewlines" || e.CreatedAt != 100 {
				t.Fatalf("lost fields: %+v", e)
			}
			for _, invalid := range []string{`complaintquery`, `complaintquery data={}{}`, `complaintquery data={"after_id":-1}`, `complaintquery data={"limit":101}`, `complaintquery data={"limit":-1}`, `complaintquery data={"actor_id":1}`, `complaintquery data={} extra=1`, `complaintquery data={} extra`, `complaintclear data={}`, `complaintclear data={"target_unique_id":"target","actor_id":1}`, `complaintclear data={} extra`} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			clear := netproto.ComplaintClear{TargetUniqueID: "target", FromUniqueID: "reporter"}
			for _, deleted := range []int64{2, 0} {
				rows := execute(inspectionCommand(t, "complaintclear", clear))
				var result netproto.ComplaintClearResult
				if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), &result); err != nil || result.Deleted != deleted {
					t.Fatalf("clear result: %+v %v", result, err)
				}
			}
			b.mu.Lock()
			reads, clears, got, gotClear := b.reads, b.clears, b.request, b.clear
			b.err = auth.ErrIntegrationDenied
			b.mu.Unlock()
			if reads != 1 || clears != 2 || got != q || gotClear != clear {
				t.Fatalf("unexpected backend calls: %d %d %+v %+v", reads, clears, got, gotClear)
			}
			for _, cmd := range []string{"complaintquery data={}", inspectionCommand(t, "complaintclear", clear)} {
				if transport == "query" {
					if got := lastErr(t, execute("login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
						t.Fatal(got)
					}
				}
				if got := lastErr(t, execute(cmd)); !strings.Contains(got, "integration\\saccess\\sdenied") {
					t.Fatal(got)
				}
			}
		})
	}
}
