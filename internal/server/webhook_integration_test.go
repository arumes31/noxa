//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

type webhookBanAuth struct {
	AuthBackend
	real *auth.AuthService
}

func (a webhookBanAuth) LookupActiveBan(ctx context.Context, uid, ip string) (*auth.Ban, error) {
	return a.real.LookupActiveBan(ctx, uid, ip)
}

func TestIntegrationWebhookTCPManagementHTTPEncryptedDeliveryAndRevocation(t *testing.T) {
	db := integrationManagementStore(t)
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO channels(id,name,channel_type) VALUES(1,'Hooks',2); INSERT INTO users(id,unique_id,nickname) VALUES(1,'admin-uid','admin')`); err != nil {
		t.Fatal(err)
	}
	policy := serverRoleFixture()
	caps := []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageChannels, authorization.SendMessages}
	policy.policy.Roles[0].Permissions = caps
	authority, err := authorization.NewAuthority(t.Context(), policy, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Chat = db
		d.ScopeKeys = db
		d.Authority = authority
		d.Auth = webhookBanAuth{d.Auth, auth.New(db, zap.NewNop())}
	})
	defer env.stop()
	env.state.AddChannel(&state.Channel{ChannelID: 1, Name: "Hooks"})
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	manage := func(r netproto.WebhookRequest) netproto.WebhookResult {
		t.Helper()
		r.ChannelID = 1
		send(t, conn, netproto.MsgWebhookRequest, r)
		var out netproto.WebhookResult
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebhookResult), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	created := manage(netproto.WebhookRequest{Action: "create", Name: "Build results"})
	if created.ID <= 0 || len(created.Token) != 43 {
		t.Fatalf("create=%+v", created)
	}
	var stored []byte
	if err := db.DB().QueryRowContext(t.Context(), `SELECT token_hash FROM incoming_webhooks WHERE id=$1`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(created.Token))
	if !bytes.Equal(stored, sum[:]) || bytes.Contains(stored, []byte(created.Token)) {
		t.Fatal("token not hashed")
	}
	if listed := manage(netproto.WebhookRequest{Action: "list"}); listed.Token != "" || len(listed.Hooks) != 1 {
		t.Fatalf("list=%+v", listed)
	}
	httpServer := httptest.NewServer(env.srv.IncomingWebhookHandler())
	defer httpServer.Close()
	post := func(token, body string, want int) int64 {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "POST", fmt.Sprintf("%s/hooks/%d", httpServer.URL, created.ID), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("post status=%d want%d body=%s", res.StatusCode, want, raw)
		}
		var out struct {
			MessageID int64 `json:"message_id"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.MessageID
	}
	post(strings.Repeat("a", 43), `{"content":"Unauthorized"}`, 401)
	message := post(created.Token, `{"content":"Build passed"}`, 201)
	row, err := db.GetChatMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	if row.BodyEnc == "Build passed" || row.KeyID == 0 || row.FromUniqueID != fmt.Sprintf("webhook:%d", created.ID) || row.FromNickname != "Build results [Webhook]" {
		t.Fatalf("stored=%+v", row)
	}
	plain, err := env.srv.chatKeys.open(t.Context(), 1, row.KeyID, row.BodyEnc)
	if err != nil || plain != "Build passed" {
		t.Fatalf("decrypt=%q %v", plain, err)
	}
	for i := 0; i < 4; i++ {
		post(created.Token, `{"content":"Another build"}`, 201)
	}
	post(created.Token, `{"content":"Too fast"}`, 429)
	if _, err := db.DB().ExecContext(t.Context(), `UPDATE incoming_webhooks SET rate_reset=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	policy.mu.Lock()
	policy.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageChannels}
	policy.policy.Revision++
	policy.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	post(created.Token, `{"content":"Permission removed"}`, 401)
	policy.mu.Lock()
	policy.policy.Roles[0].Permissions = caps
	policy.policy.Revision++
	policy.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO bans(ban_type,value) VALUES(1,'admin-uid')`); err != nil {
		t.Fatal(err)
	}
	post(created.Token, `{"content":"Banned creator"}`, 401)
	if _, err := db.DB().ExecContext(t.Context(), `DELETE FROM bans WHERE value='admin-uid'`); err != nil {
		t.Fatal(err)
	}
	manage(netproto.WebhookRequest{Action: "revoke", ID: created.ID})
	post(created.Token, `{"content":"Revoked"}`, 401)
	var count int
	if err := db.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM chat_messages`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("stored count=%d err=%v", count, err)
	}
}
