package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
)

func TestAbandonedRecoveryReleasesOnlyCandidateObligations(t *testing.T) {
	ns := Namespace{"tls-sha256:" + strings.Repeat("c", 64), "alice", "device"}
	s, err := Open(t.TempDir(), ns, IdentityProtection(bytes.Repeat([]byte{8}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	core, err := ratchet.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := core.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	_, secret, _ := ed25519.GenerateKey(rand.Reader)
	old := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 20})
	oldOTKs := map[string]OTKObligation{}
	oldRefs := []Ref{{Account, "local"}, {Publication, "old"}}
	for _, k := range old.Bundle.OneTimeKeys {
		id := "old-" + k.ID
		oldOTKs[id] = OTKObligation{PublicationID: "old", KeyID: k.ID, PublicKey: k.Key}
		oldRefs = append(oldRefs, Ref{Obligation, id})
	}
	if _, err := s.CommitPublication(readTest(t, s, Ref{Account, "local"}), "old", []byte(old.Account), []byte("old publication"), oldOTKs); err != nil {
		t.Fatal(err)
	}
	preserved := readTest(t, s, oldRefs...)
	for cycle := 0; cycle < 9; cycle++ {
		accountID, transitionID := fmt.Sprintf("candidate-%d", cycle), fmt.Sprintf("recovery-%d", cycle)
		fresh := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: s.CoreKey(), Count: 20})
		b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: ns.Server, Principal: ns.Principal, DeviceID: ns.Device,
			AccountEpoch: 4, Generation: 8, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 3600, Identity: fresh.Bundle.Identity, Signing: fresh.Bundle.Signing}
		for _, k := range fresh.Bundle.OneTimeKeys {
			b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: k.ID, Key: k.Key})
		}
		b, err = b.Sign(secret)
		if err != nil {
			t.Fatal(err)
		}
		digest, _ := olmdir.PublicationDigest(b)
		r, err := (olmdir.Reset{Version: olmdir.ResetVersion, Server: ns.Server, Principal: ns.Principal, Device: ns.Device, TransitionID: transitionID,
			OldEpoch: 3, OldIdentity: old.Bundle.Identity, OldSigning: old.Bundle.Signing, NewEpoch: 4, NewIdentity: b.Identity, NewSigning: b.Signing,
			Generation: 8, PublicationDigest: digest, Reason: "restored"}).Sign(secret)
		if err != nil {
			t.Fatal(err)
		}
		otks := map[string]OTKObligation{}
		refs := []Ref{{Transition, transitionID}, {Account, accountID}}
		for i, k := range b.OneTimeKeys {
			// Deliberately not accountID:keyID: obligation references are opaque.
			id := fmt.Sprintf("opaque-%d-%d", cycle, i)
			otks[id] = OTKObligation{AccountID: accountID, AccountEpoch: 4, PublicationID: transitionID, KeyID: k.ID, PublicKey: k.Key}
			refs = append(refs, Ref{Obligation, id})
		}
		if err := s.PrepareRecoveryTransition(readTest(t, s), accountID, []byte(fresh.Account), r, b, otks); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		pending := readTest(t, s, refs...)
		// An uncertain/prepared intent retains every key until explicit evidence.
		for id := range otks {
			var o OTKObligation
			_ = json.Unmarshal(pending.Records[Ref{Obligation, id}].Data, &o)
			if o.Closed || o.Consumed {
				t.Fatal("pending recovery lost a key")
			}
		}
		if cycle == 8 {
			receipt := olmdir.ResetReceipt{Server: ns.Server, Device: ns.Device, TransitionID: transitionID, AccountEpoch: 4, Digest: admission.Digest(encode(r)), PublicationDigest: digest}
			if err := s.PromoteAccount(pending, transitionID, receipt); err != nil {
				t.Fatal(err)
			}
			if err := s.AbandonRecoveryTransition(readTest(t, s, refs...), transitionID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("abandoned applied recovery: %v", err)
			}
			for id := range otks {
				if got := readTest(t, s, Ref{Obligation, id}).Records[Ref{Obligation, id}]; !bytes.Equal(got.Data, pending.Records[Ref{Obligation, id}].Data) {
					t.Fatal("applied recovery key changed")
				}
			}
		} else {
			if err := s.AbandonRecoveryTransition(pending, transitionID); err != nil {
				t.Fatal(err)
			}
			closed := readTest(t, s, refs...)
			for id := range otks {
				var o OTKObligation
				_ = json.Unmarshal(closed.Records[Ref{Obligation, id}].Data, &o)
				if !o.Closed || o.Consumed || o.ClosureProof != "abandoned-recovery:"+transitionID {
					t.Fatalf("candidate key not closed: %+v", o)
				}
			}
			if err := s.AbandonRecoveryTransition(closed, transitionID); err != nil {
				t.Fatal(err)
			}
			repeated := readTest(t, s, refs...)
			for _, ref := range refs {
				if repeated.Records[ref].Revision != closed.Records[ref].Revision {
					t.Fatal("abandonment is not idempotent")
				}
			}
			if !bytes.Equal(closed.Records[Ref{Account, accountID}].Data, []byte(fresh.Account)) {
				t.Fatal("candidate account erased")
			}
		}
		for _, ref := range oldRefs {
			got := readTest(t, s, ref).Records[ref]
			if got.Revision != preserved.Records[ref].Revision || !bytes.Equal(got.Data, preserved.Records[ref].Data) {
				t.Fatal("old receive state changed", ref)
			}
		}
		var authority control
		if err := s.db.View(func(tx *bolt.Tx) error {
			var err error
			authority, err = s.control(tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		wantOutstanding := 20
		if cycle == 8 {
			wantOutstanding = 40
		}
		if authority.Outstanding != wantOutstanding || authority.SendingBlocked {
			t.Fatalf("cycle %d authority: %+v", cycle, authority)
		}
	}
}
