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

// 1c-iii-b item 2: an explicit recovery moves a restored store (local epoch 1)
// to the server's epoch E+1 through a signed transition; an ordinary
// transition still requires the exact local epoch, and a non-recovery reason
// can never use the recovery path.
func TestRecoveryTransitionFromOlderLocalEpoch(t *testing.T) {
	ns := Namespace{"tls-sha256:" + strings.Repeat("b", 64), "alice", "device"}
	s, e := Open(t.TempDir(), ns, IdentityProtection(bytes.Repeat([]byte{9}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	core, e := ratchet.New(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = core.Close(context.Background()) }()
	lost := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 1}) // the server's epoch-3 account
	fresh := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 1})
	_, secret, _ := ed25519.GenerateKey(rand.Reader)
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: ns.Server, Principal: ns.Principal, DeviceID: ns.Device, AccountEpoch: 4, Generation: 8, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 3600, Identity: fresh.Bundle.Identity, Signing: fresh.Bundle.Signing, OneTimeKeys: []olmdir.Key{{ID: fresh.Bundle.OneTimeKeys[0].ID, Key: fresh.Bundle.OneTimeKeys[0].Key}}}
	tr, _ := b.Transcript()
	b.Signature = ed25519.Sign(secret, tr)
	digest, _ := olmdir.PublicationDigest(b)
	reset := func(reason string) olmdir.Reset {
		r, e := (olmdir.Reset{Version: olmdir.ResetVersion, Server: ns.Server, Principal: ns.Principal, Device: ns.Device, TransitionID: "pub:epoch-4:0000000008", OldEpoch: 3, OldIdentity: lost.Bundle.Identity, OldSigning: lost.Bundle.Signing, NewEpoch: 4, NewIdentity: b.Identity, NewSigning: b.Signing, Generation: 8, PublicationDigest: digest, Reason: reason}).Sign(secret)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	k := b.OneTimeKeys[0]
	otks := map[string]OTKObligation{"epoch-4:" + k.ID: {AccountID: "epoch-4", AccountEpoch: 4, PublicationID: "pub:epoch-4:0000000008", KeyID: k.ID, PublicKey: k.Key, ExpiresAt: b.ExpiresAt}}
	sn := readTest(t, s)
	if e = s.PrepareAccountTransition(sn, "epoch-4", []byte(fresh.Account), reset("restored"), b, otks); !errors.Is(e, ErrFenced) {
		t.Fatalf("ordinary transition from an older local epoch: %v", e)
	}
	rotated := reset("restored")
	rotated.Reason = "rotated"
	if e = s.PrepareRecoveryTransition(sn, "epoch-4", []byte(fresh.Account), rotated, b, otks); e == nil {
		t.Fatal("recovery path accepted a non-recovery reason")
	}
	r := reset("restored")
	if e = s.PrepareRecoveryTransition(sn, "epoch-4", []byte(fresh.Account), r, b, otks); e != nil {
		t.Fatal(e)
	}
	if a, _ := s.AccountAuthority(sn.Fence); !a.Blocked || a.Epoch != 1 {
		t.Fatalf("sending must stop until promotion: %+v", a)
	}
	receipt := olmdir.ResetReceipt{Server: ns.Server, Device: ns.Device, TransitionID: r.TransitionID, AccountEpoch: 4, Digest: admission.Digest(encode(r)), PublicationDigest: digest}
	sn = readTest(t, s, Ref{Transition, r.TransitionID})
	if e = s.PromoteAccount(sn, r.TransitionID, receipt); e != nil {
		t.Fatal(e)
	}
	a, e := s.AccountAuthority(sn.Fence)
	if e != nil || a.Epoch != 4 || a.AccountID != "epoch-4" || a.Blocked {
		t.Fatalf("after recovery: %+v %v", a, e)
	}
}
