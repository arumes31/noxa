package query

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/state"
)

// The nil legacy Backend deliberately panics if role dispatch falls through.
// Identity issuance and actual filtering are covered by PostgreSQL/server tests.
type roleQueryBackend struct {
	Backend
	mu          sync.Mutex
	denied      bool
	hidden      bool
	readTimeout time.Duration
	large       bool
	oversized   bool
}

func (*roleQueryBackend) RoleIntegrationsEnabled() bool { return true }
func (*roleQueryBackend) AuthenticateIntegration(_ context.Context, name, password, ip string) (auth.IntegrationPrincipal, error) {
	if name != "integration" || password != "pw" || ip != "127.0.0.1" {
		return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
	}
	return auth.IntegrationPrincipal{}, nil
}
func (b *roleQueryBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.denied {
		return auth.ErrIntegrationDenied
	}
	if b.readTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.readTimeout)
		defer cancel()
	}
	snapshot := &broadcast.TreeSnapshot{}
	if !b.hidden {
		node := &broadcast.ChannelNode{Channel: state.Channel{ChannelID: 2, Name: "visible child", Topic: "safe topic", ClientCount: 1},
			Clients: []*broadcast.ClientInfo{{ClientID: "c-visible", Nickname: "member", ChannelID: 2}}}
		snapshot.RootChannels = []*broadcast.ChannelNode{node}
		if b.large {
			node.Name = strings.Repeat("visible", 300)
			for range 2000 {
				snapshot.RootChannels = append(snapshot.RootChannels, node)
			}
		}
		if b.oversized {
			// The data row fits exactly; its final status line exceeds the cap.
			const rowWithoutName = "cid=2 pid=0 channel_name= channel_type=0 total_clients=1\n"
			node.Name = strings.Repeat("x", maxIntegrationResponseBytes-len(rowWithoutName))
		}
	}
	return deliver(ctx, snapshot)
}

func TestRoleQueryRejectsOversizedResponseBeforeDelivery(t *testing.T) {
	b := &roleQueryBackend{oversized: true}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}
	lines := sendCmd(t, conn, reader, "channellist")
	if len(lines) != 1 || !strings.Contains(lines[0], "integration\\sauthorization\\sunavailable") {
		t.Fatalf("oversized response was partially delivered: %d lines", len(lines))
	}
}

func TestRoleQueryReadsAndNoLegacyFallback(t *testing.T) {
	b := &roleQueryBackend{}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	command := func(line string) string { return strings.Join(sendCmd(t, conn, reader, line), "\n") }
	if got := command("login integration pw authorization_model=roles-v1"); !strings.Contains(got, "error id=0") {
		t.Fatal(got)
	}
	for _, tc := range []struct{ command, want string }{
		{"channellist", "cid=2 pid=0 channel_name=visible\\schild"},
		{"clientlist", "clid=c-visible"},
		{"channelinfo cid=2", "channel_topic=safe\\stopic"},
		{"channelinfo cid=1", "channel\\snot\\sfound"},
		{"servergroupadd name=admin", "command\\sunavailable"},
		{"clientmove clid=c-visible cid=1", "command\\sunavailable"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := command(tc.command); !strings.Contains(got, tc.want) {
				t.Fatalf("%s: %s", tc.command, got)
			}
		})
	}
	b.mu.Lock()
	b.hidden = true
	b.mu.Unlock()
	if got := command("channellist"); got != "error id=0 msg=ok" {
		t.Fatalf("stale result: %s", got)
	}
	b.mu.Lock()
	b.denied = true
	b.mu.Unlock()
	if got := command("clientlist"); !strings.Contains(got, "integration\\saccess\\sdenied") {
		t.Fatal(got)
	}
	if got := command("clientlist"); !strings.Contains(got, "not\\slogged\\sin") {
		t.Fatal(got)
	}
	if got := command("login integration bad authorization_model=roles-v1"); !strings.Contains(got, "invalid\\sloginname") {
		t.Fatal(got)
	}
}

func TestRoleSSHReadsRecheckEachChannel(t *testing.T) {
	b := &roleQueryBackend{}
	addr := startSSHQuery(t, b)
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	command := func(line string) string {
		t.Helper()
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer closeServerQueryTestResource(t, session)
		if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
			t.Fatal(err)
		}
		out, err := session.Output(line)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := command("channellist"); !strings.Contains(got, "cid=2 pid=0") {
		t.Fatal(got)
	}
	b.mu.Lock()
	b.hidden = true
	b.mu.Unlock()
	if got := command("channellist"); strings.Contains(got, "cid=") {
		t.Fatal(got)
	}
	b.mu.Lock()
	b.denied = true
	b.mu.Unlock()
	if got := command("clientlist"); !strings.Contains(got, "integration\\saccess\\sdenied") {
		t.Fatal(got)
	}
}

func TestRoleSSHSlowReaderReleasesDelivery(t *testing.T) {
	b := &roleQueryBackend{large: true, readTimeout: time.Second}
	addr := startSSHQuery(t, b)
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	channel, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, channel)
	go ssh.DiscardRequests(requests)
	if ok, err := channel.SendRequest("env", true, ssh.Marshal(struct{ Name, Value string }{"NOXA_AUTHORIZATION_MODEL", "roles-v1"})); err != nil || !ok {
		t.Fatalf("authorization model: %v %v", ok, err)
	}
	if _, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"channellist"})); err != nil {
		t.Fatal(err)
	}
	// Never read channel data or replenish its SSH receive window.
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
		// Peer close can arrive before the server's writer unwinds. Wait for
		// that bounded cleanup instead of assuming scheduler ordering.
		released := make(chan struct{})
		go func() { b.mu.Lock(); defer b.mu.Unlock(); close(released) }()
		select {
		case <-released:
		case <-time.After(time.Second):
			t.Fatal("delivery retained snapshot lock after transport closed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow SSH reader pinned delivery beyond its deadline")
	}
}
