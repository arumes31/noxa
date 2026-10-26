package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"strings"
	"testing"
	"time"
)

func TestAccountTransitionFencesAndPromotesExactReceipt(t *testing.T) {
	ns := Namespace{"tls-sha256:" + strings.Repeat("a", 64), "alice", "device"}
	s, e := Open(t.TempDir(), ns, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	core, e := ratchet.New(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close(context.Background())
	old := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 1})
	fresh := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 1})
	_, secret, _ := ed25519.GenerateKey(rand.Reader)
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: ns.Server, Principal: ns.Principal, DeviceID: ns.Device, AccountEpoch: 2, Generation: 2, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 3600, Identity: fresh.Bundle.Identity, Signing: fresh.Bundle.Signing, OneTimeKeys: []olmdir.Key{{ID: fresh.Bundle.OneTimeKeys[0].ID, Key: fresh.Bundle.OneTimeKeys[0].Key}}}
	tr, e := b.Transcript()
	if e != nil {
		t.Fatal(e)
	}
	b.Signature = ed25519.Sign(secret, tr)
	digest, _ := olmdir.PublicationDigest(b)
	r, e := (olmdir.Reset{Version: olmdir.ResetVersion, Server: ns.Server, Principal: ns.Principal, Device: ns.Device, TransitionID: "reset", OldEpoch: 1, OldIdentity: old.Bundle.Identity, OldSigning: old.Bundle.Signing, NewEpoch: 2, NewIdentity: b.Identity, NewSigning: b.Signing, Generation: 2, PublicationDigest: digest, Reason: "restored"}).Sign(secret)
	if e != nil {
		t.Fatal(e)
	}
	sn := readTest(t, s, Ref{Account, "local"})
	key := old.Bundle.OneTimeKeys[0]
	if _, e = s.CommitPublication(sn, "oldpub", []byte(old.Account), []byte("old verified publication"), map[string]OTKObligation{"oldkey": {PublicationID: "oldpub", KeyID: key.ID, PublicKey: key.Key, ExpiresAt: b.ExpiresAt}}); e != nil {
		t.Fatal(e)
	}
	sn = readTest(t, s)
	token, e := s.AccountAuthority(sn.Fence)
	if e != nil {
		t.Fatal(e)
	}
	k := b.OneTimeKeys[0]
	otks := map[string]OTKObligation{"newkey": {AccountID: "epoch-2", AccountEpoch: 2, PublicationID: r.TransitionID, KeyID: k.ID, PublicKey: k.Key, ExpiresAt: b.ExpiresAt}}
	if e = s.PrepareAccountTransition(sn, "epoch-2", []byte(fresh.Account), r, b, otks); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckAccountAuthority(token); !errors.Is(e, ErrFenced) {
		t.Fatal("old send authority survived", e)
	}
	receipt := olmdir.ResetReceipt{Server: ns.Server, Device: ns.Device, TransitionID: r.TransitionID, AccountEpoch: 2, Digest: admission.Digest(encode(r)), PublicationDigest: digest}
	sn = readTest(t, s, Ref{Transition, r.TransitionID})
	bad := receipt
	bad.Digest = "wrong"
	if e = s.PromoteAccount(sn, r.TransitionID, bad); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.PromoteAccount(sn, r.TransitionID, receipt); e != nil {
		t.Fatal(e)
	}
	sn = readTest(t, s, Ref{Account, "local"}, Ref{Account, "epoch-2"}, Ref{Obligation, "oldkey"})
	if !bytes.Equal(sn.Records[Ref{Account, "local"}].Data, []byte(old.Account)) || sn.Records[Ref{Obligation, "oldkey"}].Revision != 1 {
		t.Fatal("reset removed pending old account/key")
	}
	current, e := s.AccountAuthority(sn.Fence)
	if e != nil || current.Epoch != 2 || current.AccountID != "epoch-2" || current.Blocked || current.NextAttempt != 0 {
		t.Fatal(current, e)
	}
}
