package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
)

// F5: a retry from a new consumer (after a reconnect or restart) first
// replays the durable claim reservation for the same device, so the server
// hands out the same key instead of another; only if that replay fails is a
// new claim reserved.
func TestRatchetRetryReplaysDurableClaim(t *testing.T) {
	ns := durable.Namespace{Server: "tls-sha256:" + strings.Repeat("e", 64), Principal: "alice", Device: "a1"}
	s, err := durable.Open(t.TempDir(), ns, durable.IdentityProtection(bytes.Repeat([]byte{9}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bobKey, _, _ := ed25519.GenerateKey(rand.Reader)
	x, _ := ecdh.X25519().GenerateKey(rand.Reader)
	bobIdentity := base64.RawStdEncoding.EncodeToString(x.PublicKey().Bytes())
	var claims []string
	consumer := func() *ratchetConsumer {
		var c *ratchetConsumer
		c, err := newRatchetConsumer(ratchetDeps{Store: s, Core: ratchetFakeCore{}, Namespace: ns, AttemptTTL: 300,
			Admission: func(_ context.Context, r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
				a, err := c.authority()
				if err != nil {
					return nil, err
				}
				return json.Marshal(ratchetRoster{Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}},
					Targets: []admission.Target{{Principal: "alice", Device: "a1", AccountEpoch: a.Epoch, Identity: c.identity, Local: true},
						{Principal: "bob", Device: "b1", AccountEpoch: 1, Identity: bobIdentity}}})
			},
			Directory: func(_ context.Context, r netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
				if r.Action == "claim" {
					claims = append(claims, r.RequestID)
				}
				return netproto.OlmDirectoryResult{}, rfail("unavailable") // the reply is lost
			},
			DeviceKey:     func(context.Context, string, string) (ed25519.PublicKey, error) { return bobKey, nil },
			SignBundle:    func(b olmdir.Bundle) (olmdir.Bundle, error) { return b, nil },
			AssertCurrent: func() error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	send := func(c *ratchetConsumer) {
		a, err := c.authority()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.seal(a, ratchetIntent{LogicalID: "hello", Scope: "direct", Peer: "bob", Body: json.RawMessage(`{"text":"hi"}`)}, false); ratchetCode(err) != "unavailable" {
			t.Fatalf("send: %v", err)
		}
	}
	send(consumer())
	send(consumer()) // fresh in-memory state: a reconnect or restart
	if len(claims) != 3 || claims[1] != claims[0] || claims[2] == claims[0] {
		t.Fatalf("claim requests %v: want the durable claim replayed, then one new claim", claims)
	}
}
