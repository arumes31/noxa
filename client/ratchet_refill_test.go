package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

// F4: readiness refills one-time keys as soon as the server reports few
// claimable keys, never past the Olm account's 100 private keys (the real core
// refuses that, which would fail readiness), and keeps the old time-only rule
// when the server does not report the count.
func TestRatchetRefillFollowsClaimableKeys(t *testing.T) {
	engine, err := ratchet.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		available *int
		readies   int
		want      []int64
	}{
		{name: "unknown count", readies: 2, want: []int64{1, 1}},
		{name: "enough keys", available: new(10), readies: 2, want: []int64{1, 1}},
		// 20 initial keys + 8 refills of 10 fill the account's 100 keys.
		{name: "busy device", available: new(0), readies: 10, want: []int64{1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := durable.Namespace{Server: "tls-sha256:" + strings.Repeat("e", 64), Principal: "alice", Device: "a1"}
			s, err := durable.Open(t.TempDir(), ns, durable.IdentityProtection(bytes.Repeat([]byte{9}, 32)), 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			var published []int64
			c, err := newRatchetConsumer(ratchetDeps{Store: s, Core: engine, Namespace: ns, AttemptTTL: 300,
				Admission: func(context.Context, netproto.OlmAdmissionRequest) (json.RawMessage, error) { return json.Marshal([]any{}) },
				Directory: func(_ context.Context, r netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
					if r.Action != "publish" {
						t.Fatalf("unexpected directory %s", r.Action)
					}
					published = append(published, r.Bundle.Generation)
					return netproto.OlmDirectoryResult{RequestID: r.RequestID, Action: r.Action, Available: tc.available}, nil
				},
				DeviceKey:     func(context.Context, string, string) (ed25519.PublicKey, error) { return nil, rfail("untrusted_device") },
				SignBundle:    func(b olmdir.Bundle) (olmdir.Bundle, error) { return b, nil },
				AssertCurrent: func() error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			for range tc.readies {
				if _, err := c.readyOnce(); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(published, tc.want) {
				t.Fatalf("published generations %v, want %v", published, tc.want)
			}
		})
	}
}
