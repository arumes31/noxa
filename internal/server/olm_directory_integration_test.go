//go:build integration

package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationOlmDirectorySessionAuthority(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	db := integrationManagementStore(t)
	backend := serverRoleFixture()
	authority, e := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Devices: db})
	srv.tlsFingerprint = strings.TrimSuffix(strings.Repeat("aa:", 32), ":")
	makeClient := func(principal string) (*Client, ed25519.PrivateKey) {
		pub, priv, e := auth.GenerateIdentityKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		d, e := db.AddDevice(t.Context(), store.NewDevice{Principal: principal, Ed25519Public: pub, Kind: store.DeviceKindDesktop}, 24)
		if e != nil {
			t.Fatal(e)
		}
		secret, _ := auth.LoadPrivateKey(priv)
		return &Client{UniqueID: principal, authed: true, deviceID: d.DeviceID, authSigningKey: pub}, secret
	}
	recipient, secret := makeClient("recipient-guest")
	sender, _ := makeClient("sender-guest")
	var fixture struct{ Bundle olmdir.Bundle }
	raw, e := os.ReadFile("../e2ee/olmdir/testdata/directory-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	b := fixture.Bundle
	b.Principal = recipient.UniqueID
	b.DeviceID = recipient.deviceID
	b.IssuedAt = time.Now().Unix()
	b.ExpiresAt = b.IssuedAt + 3600
	tr, _ := b.Transcript()
	b.Signature = ed25519.Sign(secret, tr)
	call := func(c *Client, r netproto.OlmDirectoryRequest) netproto.OlmDirectoryResult {
		t.Helper()
		conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
		c.Conn = conn
		raw, _ := json.Marshal(r)
		if e := srv.handleOlmDirectory(t.Context(), c, &netproto.Frame{Payload: raw}); e != nil {
			t.Fatal(e)
		}
		f, e := netproto.ReadFrame(&conn.output)
		if e != nil {
			t.Fatal(e)
		}
		if f.Type != uint16(netproto.MsgOlmDirectoryResult) {
			t.Fatalf("unexpected frame %d %s", f.Type, f.Payload)
		}
		var out netproto.OlmDirectoryResult
		if e = netproto.Decode(f, &out); e != nil {
			t.Fatal(e)
		}
		if out.RequestID != r.RequestID {
			t.Fatal("uncorrelated response")
		}
		return out
	}
	pub := netproto.OlmDirectoryRequest{RequestID: "pub", Action: "publish", Bundle: &b}
	if out := call(sender, pub); out.Error != "authority-required" {
		t.Fatalf("another device publication: %+v", out)
	}
	saved := recipient.authSigningKey
	recipient.authSigningKey = sender.authSigningKey
	if out := call(recipient, pub); out.Error != "authority-required" {
		t.Fatalf("session signing key mismatch: %+v", out)
	}
	recipient.authSigningKey = saved
	if out := call(recipient, pub); out.Error != "" {
		t.Fatalf("signed UserID0 guest publication: %+v", out)
	}
	query := netproto.OlmDirectoryRequest{RequestID: "claim", ClaimUntil: b.ExpiresAt, Action: "claim", Principal: b.Principal, DeviceID: b.DeviceID}
	if out := call(sender, query); out.Error != "" || out.Bundle == nil || out.KeyID == "" {
		t.Fatalf("claim: %+v", out)
	}
	sender.rulesPending = true
	if out := call(sender, query); out.Error != "authority-required" {
		t.Fatalf("rules bypass: %+v", out)
	}
	sender.rulesPending = false
	sender.authSigningKey = ""
	if out := call(sender, query); out.Error != "authority-required" {
		t.Fatalf("unsigned guest bypass: %+v", out)
	}
	if _, e := db.RevokeDevice(t.Context(), b.Principal, b.DeviceID, store.DeviceRevokedByUser); e != nil {
		t.Fatal(e)
	}
	if out := call(recipient, pub); out.Error != "authority-required" {
		t.Fatalf("revoked publication: %+v", out)
	}
	srv.tlsFingerprint = ""
	if _, _, ok := srv.olmDirectory(); ok {
		t.Fatal("noTLS capability advertised")
	}
}
