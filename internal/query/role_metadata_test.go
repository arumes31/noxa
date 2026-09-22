package query

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type metadataQueryBackend struct {
	*roleQueryBackend
	err         error
	calls       int
	saves       int
	saved       netproto.ServerConfig
	filterCalls int
	textCalls   int
	customCalls int
}

func (b *metadataQueryBackend) SetIntegrationServerText(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.ServerTextSet) (netproto.ServerTextResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.textCalls++
	if b.err != nil {
		return netproto.ServerTextResult{}, b.err
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
		return netproto.ServerTextResult{}, fmt.Errorf("missing bounded operation context")
	}
	return netproto.ServerTextResult{Key: request.Key, ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(*request.Value)))}, nil
}

func (b *metadataQueryBackend) WithIntegrationChatFilters(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ChatFilterResponse) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filterCalls++
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.ChatFilterResponse{WordFilter: "Words | with\nnewlines", LinkBlacklist: "blocked.example", FromConfig: true})
}

func (b *metadataQueryBackend) SetIntegrationChatFilters(_ context.Context, _ auth.IntegrationPrincipal, patch netproto.ChatFilterSet) (netproto.ChatFilterResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filterCalls++
	if b.err != nil {
		return netproto.ChatFilterResponse{}, b.err
	}
	if patch.WordFilter == nil || *patch.WordFilter != "" || patch.LinkBlacklist != nil || patch.LinkWhitelist != nil {
		return netproto.ChatFilterResponse{}, authorization.ErrRoleInvalid
	}
	return netproto.ChatFilterResponse{LinkBlacklist: "blocked.example"}, nil
}

func (b *metadataQueryBackend) SetIntegrationServerConfig(_ context.Context, _ auth.IntegrationPrincipal, request netproto.ServerConfig) (netproto.ServerConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saves++
	if b.err != nil {
		return netproto.ServerConfig{}, b.err
	}
	b.saved = request
	return request, nil
}

func (b *metadataQueryBackend) WithIntegrationServerInfo(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerInfoResponse) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.ServerInfoResponse{Name: "Server | with spaces", ClientsOnline: 2, ChannelsOnline: 1})
}

func (b *metadataQueryBackend) WithIntegrationClientInfo(ctx context.Context, _ auth.IntegrationPrincipal, target string, deliver func(context.Context, netproto.ClientInfoResponse) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.ClientInfoResponse{ClientID: target, Nickname: "Member | name", PingMs: -1})
}

func (b *metadataQueryBackend) WithIntegrationServerConfig(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerConfig) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return b.err
	}
	return deliver(ctx, netproto.ServerConfig{MaxClients: 25})
}

func TestRoleMetadataQueryAndSSH(t *testing.T) {
	for _, transport := range []string{"query", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			b := &metadataQueryBackend{roleQueryBackend: &roleQueryBackend{}}
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
			decode := func(command string, value any) {
				t.Helper()
				rows := execute(command)
				if err := json.Unmarshal([]byte(unescape(strings.TrimPrefix(rows[0], "data="))), value); err != nil {
					t.Fatal(err)
				}
			}
			var server netproto.ServerInfoResponse
			decode("serverinfo", &server)
			if server.Name != "Server | with spaces" || server.ClientsOnline != 2 || server.ChannelsOnline != 1 {
				t.Fatalf("server metadata: %+v", server)
			}
			var member netproto.ClientInfoResponse
			decode("clientinfo clid="+escape("target|id"), &member)
			if member.ClientID != "target|id" || member.Nickname != "Member | name" || member.IP != "" || member.PingMs != -1 {
				t.Fatalf("member metadata: %+v", member)
			}
			var config netproto.ServerConfig
			decode("serverconfig", &config)
			if config.MaxClients != 25 {
				t.Fatalf("server configuration: %+v", config)
			}
			for _, invalid := range []string{"clientinfo", "clientinfo clid=target actor_id=1", "clientinfo clid=target extra", "serverinfo actor_id=1", "serverconfig write"} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			request := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true, OpusStereo: true}
			for _, clear := range []bool{false, true} {
				if clear {
					request.MaxClients, request.OpusFEC, request.OpusDTX, request.OpusStereo = 0, false, false, false
				}
				decode(inspectionCommand(t, "serverconfigset", request), &config)
				if config != request {
					t.Fatalf("saved acknowledgement: %+v want=%+v", config, request)
				}
			}
			for _, invalid := range []string{`serverconfigset`, `serverconfigset data={}`, `serverconfigset data={} extra=1`, `serverconfigset data={} extra`, `serverconfigset data={"actor_id":1}`, `serverconfigset data={"max_clients":-1,"client_timeout_seconds":120,"opus_bitrate":64000}`} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			var filters netproto.ChatFilterResponse
			decode("chatfilterquery", &filters)
			if filters.WordFilter != "Words | with\nnewlines" || filters.LinkBlacklist != "blocked.example" || !filters.FromConfig {
				t.Fatalf("filter read: %+v", filters)
			}
			empty := ""
			filterCommand := inspectionCommand(t, "chatfilterset", netproto.ChatFilterSet{WordFilter: &empty})
			filters = netproto.ChatFilterResponse{}
			decode(filterCommand, &filters)
			if filters.WordFilter != "" || filters.LinkBlacklist != "blocked.example" || filters.FromConfig {
				t.Fatalf("filter clear: %+v", filters)
			}
			for _, invalid := range []string{`chatfilterquery extra`, `chatfilterquery actor_id=1`, `chatfilterset`, `chatfilterset data={}`, `chatfilterset data={"word_filter":null}`, `chatfilterset data={"word_filter":"","actor_id":1}`, `chatfilterset data={} extra=1`} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			var textResult netproto.ServerTextResult
			text := "Server | text\nwith spaces and ü"
			for _, value := range []*string{&text, &empty} {
				decode(inspectionCommand(t, "servertextset", netproto.ServerTextSet{Key: "motd", Value: value}), &textResult)
				if textResult.Key != "motd" || textResult.ContentHash != fmt.Sprintf("%x", sha256.Sum256([]byte(*value))) {
					t.Fatalf("text acknowledgement: %+v", textResult)
				}
			}
			textCommand := inspectionCommand(t, "servertextset", netproto.ServerTextSet{Key: "motd", Value: &text})
			for _, invalid := range []string{`servertextset`, `servertextset data={}`, `servertextset data={"key":"motd"}`, `servertextset data={"key":"motd","value":null}`, `servertextset data={"key":"motd","value":"","actor_id":1}`, `servertextset data={"key":"owner_id","value":"1"}`, textCommand + " extra=1", textCommand + " extra"} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			customRead := inspectionCommand(t, "customquery", netproto.CustomMetadataQuery{UniqueID: "target|uid", AfterKey: "before", Limit: 1})
			var customPage netproto.CustomMetadataPage
			decode(customRead, &customPage)
			if customPage.UniqueID != "target|uid" || len(customPage.Entries) != 1 || customPage.Entries[0].Value != "value | with\nlines" || customPage.NextAfterKey != "key|ü" {
				t.Fatalf("custom page: %+v", customPage)
			}
			annotation := "value | with\nlines"
			var customWrite string
			for _, value := range []*string{&annotation, &empty, nil} {
				customWrite = inspectionCommand(t, "customchange", netproto.CustomMetadataChange{UniqueID: "target|uid", Key: "key|ü", Value: value, Delete: value == nil})
				var result netproto.CustomMetadataResult
				decode(customWrite, &result)
				if result.UniqueID != "target|uid" || result.Key != "key|ü" || result.Deleted != (value == nil) {
					t.Fatalf("custom commit: %+v", result)
				}
			}
			for _, invalid := range []string{`customquery data={}`, `customquery data={"unique_id":"uid","limit":101}`, customRead + " extra", `customchange data={"unique_id":"uid","key":"key","value":null}`, `customchange data={"unique_id":"uid","key":"key","value":"","delete":true}`, `customchange data={"unique_id":"uid","key":"key","delete":true,"actor_id":1}`, customWrite + " extra=1"} {
				if got := lastErr(t, execute(invalid)); !strings.HasPrefix(got, "error id=512 ") {
					t.Fatal(got)
				}
			}
			b.mu.Lock()
			calls, filterCalls := b.calls, b.filterCalls
			customCalls := b.customCalls
			textCalls := b.textCalls
			saves, saved := b.saves, b.saved
			b.err = authorization.ErrRoleForbidden
			b.mu.Unlock()
			if calls != 3 {
				t.Fatalf("invalid metadata request reached backend: %d", calls)
			}
			if saves != 2 || saved != request {
				t.Fatalf("invalid save reached backend or fields changed: %d %+v", saves, saved)
			}
			if filterCalls != 2 {
				t.Fatal("invalid filters reached backend or save refreshed protected data")
			}
			if textCalls != 2 {
				t.Fatal("invalid text reached backend or save refreshed protected data")
			}
			if customCalls != 4 {
				t.Fatalf("invalid custom request reached backend or write performed read: %d", customCalls)
			}
			for _, command := range []string{"serverinfo", "clientinfo clid=target", "serverconfig", inspectionCommand(t, "serverconfigset", request), "chatfilterquery", filterCommand, textCommand, customRead, customWrite} {
				rows := execute(command)
				if len(rows) != 1 || !strings.HasPrefix(rows[0], "error id=2568 ") {
					t.Fatalf("denied metadata leaked a response: %v", rows)
				}
			}
		})
	}
}
