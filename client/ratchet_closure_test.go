package main

import (
	"encoding/json"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

// 1c-iii-a item 3 (native): the one-time-key closure lifecycle asks the
// server only about expired, unconsumed, unclosed obligations, retires
// exactly the keys it authenticates as closed in the real core, and commits
// account + closure receipts together. Available/reserved keys stay open.
func TestRatchetClosesOnlyServerClosedOneTimeKeys(t *testing.T) {
	var asked [][]string
	var lifecycle func(keys []string) []durable.KeyClosure
	c, err := newTestRatchetConsumer(t, func(r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
		if r.Action != "close_keys" {
			t.Fatalf("unexpected %s", r.Action)
		}
		asked = append(asked, r.Keys)
		b, _ := json.Marshal(lifecycle(r.Keys))
		return b, nil
	}, 300)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := sharedRatchetEngine()
	if err != nil {
		t.Fatal(err)
	}
	c.d.Core = engine
	snap, err := c.d.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.d.Store.AccountAuthority(snap.Fence)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.coreApply(ratchet.Request{Op: ratchet.NewAccount, Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	accountRef := durable.Ref{Kind: durable.Account, ID: a.AccountID}
	s, _ := c.read(accountRef)
	if err = c.commitBundle(s, a, created, 1, nil); err != nil {
		t.Fatal(err)
	}
	if a, err = c.authority(); err != nil {
		t.Fatal(err)
	}
	obligations := func() map[string]durable.OTKObligation {
		refs, _ := c.refs(durable.Obligation)
		recs, _ := c.readMany(refs)
		out := map[string]durable.OTKObligation{}
		for _, ref := range refs {
			var o durable.OTKObligation
			_ = json.Unmarshal(recs[ref].Data, &o)
			out[o.KeyID] = o
		}
		return out
	}
	keys := created.Bundle.OneTimeKeys
	closedKey := keys[0].ID
	lifecycle = func(ids []string) []durable.KeyClosure {
		var out []durable.KeyClosure
		for i, id := range ids {
			state := []string{"available", "reserved"}[i%2]
			if id == closedKey {
				state = "closed"
			}
			r := durable.KeyClosure{Server: c.d.Namespace.Server, Device: c.d.Namespace.Device, AccountEpoch: a.Epoch, KeyID: id, State: state}
			b, _ := json.Marshal(r)
			r.Proof = admission.Digest(b)
			out = append(out, r)
		}
		return out
	}
	// Publication still valid: nothing is asked (a timer is never evidence,
	// and unexpired keys cannot close).
	if err = c.closeKeys(a); err != nil || len(asked) != 0 {
		t.Fatalf("unexpired: %v asked=%v", err, asked)
	}
	c.d.Now = func() time.Time { return time.Now().Add(7 * 24 * time.Hour) }
	if err = c.closeKeys(a); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || len(asked[0]) != 3 {
		t.Fatalf("asked = %v", asked)
	}
	got := obligations()
	if !got[closedKey].Closed || got[closedKey].ClosureProof == "" {
		t.Fatalf("closed key not committed: %+v", got[closedKey])
	}
	for _, k := range keys[1:] {
		if got[k.ID].Closed {
			t.Fatalf("server did not close %s", k.ID)
		}
	}
	// The private key is really gone from the committed account.
	s, _ = c.read(accountRef)
	if _, err := c.coreApply(ratchet.Request{Op: ratchet.RetirePreKey, Account: string(s.Records[accountRef].Data), OneTimeKey: keys[0].Key}); err == nil {
		t.Fatal("retired key still in the committed account")
	}
	if _, err := c.coreApply(ratchet.Request{Op: ratchet.RetirePreKey, Account: string(s.Records[accountRef].Data), OneTimeKey: keys[1].Key}); err != nil {
		t.Fatal("an open key must stay in the account", err)
	}
	// Closed obligations are never asked again.
	if err = c.closeKeys(a); err != nil || len(asked[1]) != 2 {
		t.Fatalf("second round asked %v (%v)", asked, err)
	}
	// A forged closure proof is refused atomically (nothing committed).
	closedKey = keys[1].ID
	real := lifecycle
	lifecycle = func(ids []string) []durable.KeyClosure {
		out := real(ids)
		for i := range out {
			out[i].Proof = admission.Digest([]byte("forged"))
		}
		return out
	}
	if err = c.closeKeys(a); err == nil {
		t.Fatal("forged closure accepted")
	}
	if obligations()[keys[1].ID].Closed {
		t.Fatal("forged closure committed")
	}
}
