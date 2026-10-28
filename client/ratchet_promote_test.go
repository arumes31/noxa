package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
)

func promoteKeys(t *testing.T) (string, string) {
	t.Helper()
	c, _ := ecdh.X25519().GenerateKey(rand.Reader)
	s, _, _ := ed25519.GenerateKey(rand.Reader)
	return base64.RawStdEncoding.EncodeToString(c.PublicKey().Bytes()), base64.RawStdEncoding.EncodeToString(s)
}

// 1c-iii-a item 2 (native consumer): a peer pin advances only through the
// signed reset chain from the cached pin; the old pin is kept for receiving;
// a server-asserted epoch without a valid chain blocks with account_changed.
func TestRatchetConsumerPromotesPinOnlyThroughSignedChain(t *testing.T) {
	noxaPub, noxa, _ := ed25519.GenerateKey(rand.Reader)
	server := "tls-sha256:" + strings.Repeat("e", 64)
	id1, sg1 := promoteKeys(t)
	id2, sg2 := promoteKeys(t)
	reset, err := olmdir.Reset{Version: olmdir.ResetVersion, Server: server, Principal: "dave", Device: "d1", TransitionID: "t1",
		OldEpoch: 1, OldIdentity: id1, OldSigning: sg1, NewEpoch: 2, NewIdentity: id2, NewSigning: sg2, Generation: 2,
		PublicationDigest: strings.Repeat("cd", 32), Reason: "lost"}.Sign(noxa)
	if err != nil {
		t.Fatal(err)
	}
	chain, queries := []olmdir.Reset{}, 0
	c, err := newTestRatchetConsumer(t, func(r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
		if r.Action != "reset_chain" || r.TargetDevice != "d1" {
			t.Fatalf("unexpected %+v", r)
		}
		queries++
		b, _ := json.Marshal(chain)
		return b, nil
	}, 300)
	if err != nil {
		t.Fatal(err)
	}
	c.d.DeviceKey = func(context.Context, string, string) (ed25519.PublicKey, error) { return noxaPub, nil }
	snap, err := c.d.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.d.Store.AccountAuthority(snap.Fence)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := c.trust("dave", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if trusted, err = c.adopt(trusted, olmdir.Bundle{AccountEpoch: 1, Identity: id1, Signing: sg1}); err != nil {
		t.Fatal(err)
	}
	// No chain (server asserts epoch 2): blocked, pin unchanged.
	if _, err := c.promote(a, trusted, "dave", "d1", 2, id2, false); ratchetCode(err) != "account_changed" {
		t.Fatalf("unchained epoch: %v", err)
	}
	// A chain ending at another identity: blocked.
	chain = []olmdir.Reset{reset}
	other, _ := promoteKeys(t)
	if _, err := c.promote(a, trusted, "dave", "d1", 2, other, false); ratchetCode(err) != "account_changed" {
		t.Fatalf("wrong endpoint: %v", err)
	}
	// The valid chain promotes and persists.
	promoted, err := c.promote(a, trusted, "dave", "d1", 2, id2, false)
	if err != nil || promoted.pin.Epoch != 2 || promoted.pin.Identity != id2 || promoted.pin.Signing != sg2 {
		t.Fatalf("promote: %+v %v", promoted.pin, err)
	}
	again, err := c.trust("dave", "d1")
	if err != nil || again.pin.Epoch != 2 || len(again.history) != 1 || again.history[0].Identity != id1 {
		t.Fatalf("persisted: %+v %+v %v", again.pin, again.history, err)
	}
	// Old-epoch deliveries: receive-only match on the retained pin.
	if old, err := c.promote(a, again, "dave", "d1", 1, id1, true); err != nil || old.pin.Epoch != 1 {
		t.Fatalf("historical receive: %v", err)
	}
	if _, err := c.promote(a, again, "dave", "d1", 1, id1, false); ratchetCode(err) != "account_changed" {
		t.Fatal("never SEND to a superseded pin")
	}
	// Review P3-5: only within the delayed-delivery window after retirement.
	realNow := c.d.Now
	c.d.Now = func() time.Time { return realNow().Add(8 * 24 * time.Hour) }
	if _, err := c.promote(a, again, "dave", "d1", 1, id1, true); ratchetCode(err) != "account_changed" {
		t.Fatal("a retired pin must not receive after the window")
	}
	c.d.Now = realNow
	// The promoted pin must also match the bundle's signing key.
	if _, err := c.adopt(again, olmdir.Bundle{AccountEpoch: 2, Identity: id2, Signing: sg1}); ratchetCode(err) != "account_changed" {
		t.Fatalf("signing mismatch: %v", err)
	}
	// A transition signed by any key other than the trusted noXa key: blocked.
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	id3, sg3 := promoteKeys(t)
	forged, _ := olmdir.Reset{Version: olmdir.ResetVersion, Server: server, Principal: "dave", Device: "d1", TransitionID: "t2",
		OldEpoch: 2, OldIdentity: id2, OldSigning: sg2, NewEpoch: 3, NewIdentity: id3, NewSigning: sg3, Generation: 2,
		PublicationDigest: strings.Repeat("cd", 32), Reason: "restored"}.Sign(forger)
	chain = []olmdir.Reset{forged}
	if _, err := c.promote(a, again, "dave", "d1", 3, id3, false); ratchetCode(err) != "account_changed" {
		t.Fatalf("forged: %v", err)
	}
	if queries < 3 {
		t.Fatalf("queries = %d", queries)
	}
}
