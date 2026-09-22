package query

import (
	"bufio"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/broadcast"
)

type modelQueryBackend struct {
	roleQueryBackend
	authCalls atomic.Int32
	readCalls atomic.Int32
}

func (b *modelQueryBackend) AuthenticateIntegration(ctx context.Context, name, password, ip string) (auth.IntegrationPrincipal, error) {
	b.authCalls.Add(1)
	return b.roleQueryBackend.AuthenticateIntegration(ctx, name, password, ip)
}

func (b *modelQueryBackend) WithIntegrationSnapshot(ctx context.Context, p auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.readCalls.Add(1)
	return b.roleQueryBackend.WithIntegrationSnapshot(ctx, p, deliver)
}

func TestQueryAuthorizationModelBeforeAuthentication(t *testing.T) {
	b := &modelQueryBackend{}
	addr, _ := startQueryServer(t, b)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	command := func(line string) string { return strings.Join(sendCmd(t, conn, reader, line), "\n") }
	for _, login := range []string{
		"login integration pw", "login integration pw authorization_model=roles-v2",
		"login integration pw authorization_model=roles-v1 extra", "login",
	} {
		// An incompatible retry must discard the previously authenticated identity.
		if got := command("login integration pw authorization_model=roles-v1"); !strings.Contains(got, "error id=0") || !strings.Contains(got, "authorization_model=roles-v1") {
			t.Fatalf("compatible login: %s", got)
		}
		before := b.authCalls.Load()
		if got := command(login); !strings.Contains(got, "roles-v1") || !strings.Contains(got, "upgrade") {
			t.Fatalf("%s: %s", login, got)
		}
		if b.authCalls.Load() != before {
			t.Fatal("incompatible login attempted password verification")
		}
		if got := command("channellist"); !strings.Contains(got, "not\\slogged\\sin") {
			t.Fatalf("incompatible login retained authority: %s", got)
		}
	}
	if b.readCalls.Load() != 0 {
		t.Fatal("incompatible query reached protected backend")
	}
}

func TestSSHAuthorizationModelIsPerChannel(t *testing.T) {
	b := &modelQueryBackend{}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{
		User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	for _, test := range []struct {
		name    string
		models  []string
		allowed bool
	}{
		{"missing", nil, false},
		{"future", []string{"roles-v2"}, false},
		{"compatible", []string{"roles-v1"}, true},
		{"new channel does not inherit", nil, false},
		{"incompatible replacement", []string{"roles-v1", "roles-v2"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			for _, model := range test.models {
				err := session.Setenv("NOXA_AUTHORIZATION_MODEL", model)
				if (err == nil) != (model == "roles-v1") {
					t.Fatalf("model %q: %v", model, err)
				}
			}
			before := b.readCalls.Load()
			out, err := session.Output("channellist")
			if err != nil {
				t.Fatal(err)
			}
			if test.allowed {
				if !strings.Contains(string(out), "cid=2") || b.readCalls.Load() != before+1 {
					t.Fatalf("compatible channel: %s", out)
				}
			} else if !strings.Contains(string(out), "upgrade") || !strings.Contains(string(out), "roles-v1") || b.readCalls.Load() != before {
				t.Fatalf("incompatible channel accessed backend: %s", out)
			}
		})
	}
}

func TestSSHLoginCannotReplaceEnvironmentNegotiation(t *testing.T) {
	b := &modelQueryBackend{}
	client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{
		User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServerQueryTestResource(t, client)
	for _, negotiated := range []bool{false, true} {
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = session.Close() }()
		if negotiated {
			if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
				t.Fatal(err)
			}
		}
		in, err := session.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := session.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Shell(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(out)
		for range 2 {
			if _, err := reader.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
		}
		before := b.authCalls.Load()
		if _, err := in.Write([]byte("logout\nlogin integration pw authorization_model=roles-v1\nchannellist\nquit\n")); err != nil {
			t.Fatal(err)
		}
		var response strings.Builder
		for {
			line, err := reader.ReadString('\n')
			response.WriteString(line)
			if err != nil {
				break
			}
		}
		if err := session.Wait(); err != nil {
			t.Fatal(err)
		}
		if negotiated {
			if b.authCalls.Load() != before+1 || !strings.Contains(response.String(), "cid=2") {
				t.Fatalf("negotiated relogin: %s", response.String())
			}
		} else if b.authCalls.Load() != before || strings.Contains(response.String(), "cid=") || !strings.Contains(response.String(), "upgrade") {
			t.Fatalf("shell login bypassed env negotiation: %s", response.String())
		}
	}
}
