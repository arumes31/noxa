package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

// recoveryServer mirrors store.ResetOlmAccount's rules: the reset must verify
// under the device's noXa key against the server's CURRENT state, carry the
// next generation and the request epoch must be the server's epoch.
type recoveryServer struct {
	t        *testing.T
	key      ed25519.PublicKey
	ns       durable.Namespace
	state    olmdir.Bundle // the server's current directory state for the device
	ownState *olmdir.Bundle
	resets   []olmdir.Reset
	calls    int
}

func (s *recoveryServer) directory(_ context.Context, r netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
	if r.Action != "own_state" {
		s.t.Fatalf("unexpected directory %s", r.Action)
	}
	b := s.state
	if s.ownState != nil {
		b = *s.ownState
	}
	return netproto.OlmDirectoryResult{RequestID: r.RequestID, Action: r.Action, Bundle: &b}, nil
}

func (s *recoveryServer) admission(r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
	if r.Action != "reset" || r.Reset == nil || r.Bundle == nil {
		s.t.Fatalf("unexpected admission %s", r.Action)
	}
	s.calls++
	cur := s.state
	if r.AccountEpoch != cur.AccountEpoch || r.Reset.Verify(s.key, s.ns.Server, s.ns.Principal, s.ns.Device, cur.AccountEpoch, cur.Identity, cur.Signing) != nil ||
		r.Reset.Generation != cur.Generation+1 || r.Bundle.Verify(s.key, s.ns.Server, s.ns.Principal, s.ns.Device, time.Now()) != nil {
		return nil, rfail("authority-required")
	}
	encoded, _ := json.Marshal(*r.Reset)
	digest, _ := olmdir.PublicationDigest(*r.Bundle)
	s.state = *r.Bundle
	s.resets = append(s.resets, *r.Reset)
	return json.Marshal(olmdir.ResetReceipt{Server: s.ns.Server, Device: s.ns.Device, TransitionID: r.Reset.TransitionID, AccountEpoch: r.Reset.NewEpoch, Digest: admission.Digest(encoded), PublicationDigest: digest})
}

// signedBundle publishes a fresh account at (epoch, generation) under key.
func signedBundle(t *testing.T, c *ratchetConsumer, secret ed25519.PrivateKey, epoch, generation int64) olmdir.Bundle {
	t.Helper()
	acct, err := c.coreApply(ratchet.Request{Op: ratchet.NewAccount, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	ns := c.d.Namespace
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: ns.Server, Principal: ns.Principal, DeviceID: ns.Device, AccountEpoch: epoch, Generation: generation,
		IssuedAt: time.Now().Unix() - 30*86400, ExpiresAt: time.Now().Unix() - 25*86400, Identity: acct.Bundle.Identity, Signing: acct.Bundle.Signing}
	for _, k := range acct.Bundle.OneTimeKeys {
		b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: k.ID, Key: k.Key})
	}
	b, err = b.Sign(secret)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func recoveryConsumer(t *testing.T) (*ratchetConsumer, *recoveryServer, ed25519.PrivateKey) {
	t.Helper()
	srv := &recoveryServer{t: t}
	c, err := newTestRatchetConsumer(t, srv.admission, 300)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := sharedRatchetEngine()
	if err != nil {
		t.Fatal(err)
	}
	pub, secret, _ := ed25519.GenerateKey(rand.Reader)
	c.d.Core = engine
	c.d.Directory = srv.directory
	c.d.OwnKey = pub
	c.d.SignBundle = func(b olmdir.Bundle) (olmdir.Bundle, error) { return b.Sign(secret) }
	c.d.SignReset = func(r olmdir.Reset) (olmdir.Reset, error) { return r.Sign(secret) }
	srv.key, srv.ns = pub, c.d.Namespace
	// The server's state: this device's (lost) epoch-3 account, generation 5.
	srv.state = signedBundle(t, c, secret, 3, 5)
	return c, srv, secret
}

func authorityOf(t *testing.T, c *ratchetConsumer) durable.AccountAuthority {
	t.Helper()
	s, err := c.d.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.d.Store.AccountAuthority(s.Fence)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// 1c-iii-b item 2: a restored store (local epoch 1) recovers to the server's
// epoch+1 through a signed reset that a peer holding the old pin verifies.
func TestRatchetRecoveryRestoredStoreGetsFreshSignedEpoch(t *testing.T) {
	c, srv, _ := recoveryConsumer(t)
	old := srv.state
	c.sticky = rfail("recovery_required")
	if err := c.recoverAccount("restored"); err != nil {
		t.Fatal(err)
	}
	a := authorityOf(t, c)
	if a.Epoch != 4 || a.Blocked || c.sticky != nil {
		t.Fatalf("after recovery: %+v sticky=%v", a, c.sticky)
	}
	latest, err := c.latestPublication(a)
	if err != nil || latest == nil || latest.AccountEpoch != 4 || latest.Generation != 6 || latest.Identity != srv.state.Identity {
		t.Fatalf("new publication: %+v %v", latest, err)
	}
	// A peer holding the old pin verifies the signed chain to the new one.
	pin, passed, err := olmdir.VerifyResetChain(olmdir.ResetPin{Epoch: 3, Identity: old.Identity, Signing: old.Signing},
		olmdir.ResetPin{Epoch: 4, Identity: latest.Identity, Signing: latest.Signing}, srv.key, srv.ns.Server, srv.ns.Principal, srv.ns.Device,
		func(after int64) ([]olmdir.Reset, error) { return srv.resets, nil })
	if err != nil || pin.Epoch != 4 || len(passed) != 1 {
		t.Fatalf("peer chain verification: %+v %v", pin, err)
	}
	// Recovery is explicit and one-shot: repeating it is "not needed".
	if err := c.recoverAccount("restored"); ratchetCode(err) != "not_needed" {
		t.Fatalf("second recovery: %v", err)
	}
}

// Hostile server: a forged or regressed own_state is refused before anything
// is signed or stored; a stale (older, genuinely signed) own_state builds a
// reset the server refuses and a peer could never verify, and the refused
// intent is abandoned, so no pin changes silently and recovery can start over.
func TestRatchetRecoveryRefusesHostileOwnState(t *testing.T) {
	t.Run("forged signature", func(t *testing.T) {
		c, srv, _ := recoveryConsumer(t)
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		forged := signedBundle(t, c, other, 3, 5)
		srv.ownState = &forged
		if err := c.recoverAccount("restored"); ratchetCode(err) != "invalid_evidence" {
			t.Fatalf("forged own_state: %v", err)
		}
		if a := authorityOf(t, c); a.Blocked || a.Epoch != 1 || srv.calls != 0 {
			t.Fatalf("a forged own_state changed state: %+v calls=%d", a, srv.calls)
		}
	})
	t.Run("regressed epoch", func(t *testing.T) {
		c, srv, secret := recoveryConsumer(t)
		// Move the local store to epoch 4 by a real recovery first.
		if err := c.recoverAccount("restored"); err != nil {
			t.Fatal(err)
		}
		stale := signedBundle(t, c, secret, 3, 5)
		srv.ownState = &stale
		if err := c.recoverAccount("lost"); ratchetCode(err) != "invalid_evidence" {
			t.Fatalf("own_state older than the local epoch: %v", err)
		}
	})
	t.Run("stale genuine bundle", func(t *testing.T) {
		c, srv, secret := recoveryConsumer(t)
		stale := signedBundle(t, c, secret, 2, 3) // an older publication the server replays
		srv.ownState = &stale
		if err := c.recoverAccount("restored"); ratchetCode(err) != "authority-required" {
			t.Fatalf("stale own_state: %v", err)
		}
		if a := authorityOf(t, c); a.Blocked || a.Epoch != 1 {
			t.Fatalf("a refused intent must be abandoned: %+v", a)
		}
		if len(srv.resets) != 0 || srv.state.AccountEpoch != 3 {
			t.Fatal("the server accepted a reset built from stale state")
		}
		// The honest state then recovers normally.
		srv.ownState = nil
		if err := c.recoverAccount("restored"); err != nil {
			t.Fatal(err)
		}
		if a := authorityOf(t, c); a.Epoch != 4 {
			t.Fatalf("recovery after abandonment: %+v", a)
		}
	})
}
