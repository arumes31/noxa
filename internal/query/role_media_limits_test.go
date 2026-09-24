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

type mediaLimitsQueryBackend struct {
	*roleQueryBackend
	limits netproto.MediaLimitsChanged
	denied bool
	calls  int
}

func (b *mediaLimitsQueryBackend) WithIntegrationMediaLimits(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.MediaLimitsChanged) error) error {
	b.calls++
	if b.denied {
		return authorization.ErrRoleForbidden
	}
	return deliver(ctx, b.limits)
}

func (b *mediaLimitsQueryBackend) SetIntegrationMediaLimits(ctx context.Context, _ auth.IntegrationPrincipal, limits netproto.MediaLimits) (netproto.MediaLimitsSaved, error) {
	b.calls++
	if b.denied {
		return netproto.MediaLimitsSaved{}, authorization.ErrRoleForbidden
	}
	b.limits.Revision++
	b.limits.MediaLimits = limits
	return netproto.MediaLimitsSaved(b.limits), nil
}

func TestRoleMediaLimitsQuery(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &mediaLimitsQueryBackend{roleQueryBackend: &roleQueryBackend{}, limits: netproto.MediaLimitsChanged{Revision: 3, MediaLimits: netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 1280, VideoMaxHeight: 720}}}
			var execute func(string) []string
			if transport == "query" {
				addr, _ := startQueryServer(t, b)
				conn, reader := dialQuery(t, addr)
				t.Cleanup(func() { closeServerQueryTestResource(t, conn) })
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				execute = func(command string) []string { return sendCmd(t, conn, reader, command) }
				if got := lastErr(t, execute("login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
					t.Fatal(got)
				}
			} else {
				client, err := ssh.Dial("tcp", startSSHQuery(t, b), &ssh.ClientConfig{User: "integration", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeServerQueryTestResource(t, client) })
				execute = func(command string) []string {
					session, err := client.NewSession()
					if err != nil {
						t.Fatal(err)
					}
					defer closeServerQueryTestResource(t, session)
					if err := session.Setenv("NOXA_AUTHORIZATION_MODEL", "roles-v1"); err != nil {
						t.Fatal(err)
					}
					out, err := session.Output(command)
					if err != nil {
						t.Fatal(err)
					}
					return strings.Split(strings.TrimSpace(string(out)), "\n")
				}
			}
			decode := func(command string, target any) {
				t.Helper()
				rows := execute(command)
				if len(rows) != 2 || lastErr(t, rows) != "error id=0 msg=ok" {
					t.Fatalf("%s: %v", command, rows)
				}
				if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), target); err != nil {
					t.Fatal(err)
				}
			}
			var read netproto.MediaLimitsChanged
			decode("medialimits", &read)
			if read != b.limits {
				t.Fatalf("read = %+v", read)
			}
			want := netproto.MediaLimits{VideoMaxBitrate: 400000, VideoMaxWidth: 640, VideoMaxHeight: 360}
			var saved netproto.MediaLimitsSaved
			decode(inspectionCommand(t, "medialimitsset", want), &saved)
			if saved.MediaLimits != want || saved.Revision != 4 {
				t.Fatalf("saved = %+v", saved)
			}
			for _, command := range []string{"medialimits extra", "medialimits value=1", "medialimitsset", `medialimitsset data={}`, `medialimitsset data={"video_max_bitrate":0,"video_max_width":0}`, `medialimitsset data={"video_max_bitrate":0,"video_max_width":640,"video_max_height":0}`} {
				if got := lastErr(t, execute(command)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatalf("%s: %s", command, got)
				}
			}
			if b.calls != 2 {
				t.Fatalf("invalid requests reached backend: %d", b.calls)
			}
			b.denied = true
			for _, command := range []string{"medialimits", inspectionCommand(t, "medialimitsset", want)} {
				rows := execute(command)
				if len(rows) != 1 || !strings.HasPrefix(rows[0], "error id=2568 ") {
					t.Fatalf("denied command leaked response: %v", rows)
				}
			}
		})
	}
}
