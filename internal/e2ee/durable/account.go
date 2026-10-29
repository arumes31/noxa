package durable

import (
	"bytes"
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
)

type AccountAuthority struct {
	AccountID   string `json:"account_id"`
	Epoch       int64  `json:"epoch"`
	Fence       uint64 `json:"fence"`
	Blocked     bool   `json:"blocked"`
	NextAttempt int64  `json:"next_attempt"`
	// Retired is the current epoch's durable retire_receipt watermark.
	Retired RetiredMark `json:"retired"`
}

// RetiredMark is durable retire_receipt progress for one account epoch: a
// contiguous watermark plus at most 1,024 sparse sequences above it (browser
// mirror: noteRetired / accountAuthority().retired).
type RetiredMark struct {
	Epoch   int64   `json:"epoch"`
	Through int64   `json:"through"`
	Above   []int64 `json:"above"`
}

// Has reports whether the attempt sequence is already durably retired.
func (r RetiredMark) Has(seq int64) bool {
	if seq <= r.Through {
		return true
	}
	for _, v := range r.Above {
		if v == seq {
			return true
		}
	}
	return false
}

type AccountTransition struct {
	AccountID string               `json:"account_id"`
	Reset     olmdir.Reset         `json:"reset"`
	Bundle    olmdir.Bundle        `json:"bundle"`
	Receipt   *olmdir.ResetReceipt `json:"receipt,omitempty"`
	// Recovery marks an explicit, user-initiated recovery of a restored or
	// lost local store: it may sit at an older local epoch (FromEpoch) than
	// the server's current one (Reset.OldEpoch).
	Recovery  bool  `json:"recovery,omitempty"`
	FromEpoch int64 `json:"from_epoch,omitempty"`
	// Abandoned: the server deterministically refused this recovery intent;
	// it is kept as a record but never resumed or promoted.
	Abandoned bool `json:"abandoned,omitempty"`
}
type KeyClosure struct {
	Server       string `json:"server"`
	Device       string `json:"device"`
	AccountEpoch int64  `json:"account_epoch"`
	KeyID        string `json:"key_id"`
	State        string `json:"state"`
	Proof        string `json:"proof"`
}

func (s *Store) AccountAuthority(fence uint64) (AccountAuthority, error) {
	var out AccountAuthority
	e := s.db.View(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, Snapshot{Namespace: s.ns, Fence: fence})
		if e != nil {
			return e
		}
		out = AccountAuthority{c.AccountID, c.AccountEpoch, c.Fence, c.SendingBlocked, c.NextAttempt, RetiredMark{Epoch: c.AccountEpoch, Above: []int64{}}}
		if r := c.Retired; r != nil && r.Epoch == c.AccountEpoch {
			out.Retired = RetiredMark{Epoch: r.Epoch, Through: r.Through, Above: append([]int64{}, r.Above...)}
		}
		return nil
	})
	return out, e
}
func (s *Store) CheckAccountAuthority(token AccountAuthority) error {
	now, e := s.AccountAuthority(token.Fence)
	if e != nil {
		return e
	}
	if now.AccountID != token.AccountID || now.Epoch != token.Epoch || now.Blocked {
		return ErrFenced
	}
	return nil
}

// PrepareAccountTransition stores exact signed recovery intent and fresh state,
// preserving old archives/accounts. Old sending stops in the same commit.
func (s *Store) PrepareAccountTransition(snap Snapshot, accountID string, account []byte, r olmdir.Reset, b olmdir.Bundle, otks map[string]OTKObligation) error {
	return s.prepareTransition(snap, accountID, account, r, b, otks, false)
}

// PrepareRecoveryTransition is PrepareAccountTransition for the explicit,
// user-initiated recovery of a restored or lost store (Reason "lost" or
// "restored"): the local store may be at an OLDER epoch than r.OldEpoch (the
// server's current one, read from this device's own signed publication). It
// never adopts server state as trusted: r is a fresh signed transition from
// the noXa chain that the server and every peer verify.
func (s *Store) PrepareRecoveryTransition(snap Snapshot, accountID string, account []byte, r olmdir.Reset, b olmdir.Bundle, otks map[string]OTKObligation) error {
	if r.Reason != "lost" && r.Reason != "restored" {
		return ErrInvalid
	}
	return s.prepareTransition(snap, accountID, account, r, b, otks, true)
}

func (s *Store) prepareTransition(snap Snapshot, accountID string, account []byte, r olmdir.Reset, b olmdir.Bundle, otks map[string]OTKObligation, recovery bool) error {
	if !validID(accountID) || len(account) == 0 || len(otks) != len(b.OneTimeKeys) {
		return ErrInvalid
	}
	digest, e := olmdir.PublicationDigest(b)
	if e != nil || digest != r.PublicationDigest || r.NewEpoch != b.AccountEpoch || r.Generation != b.Generation || r.NewIdentity != b.Identity || r.NewSigning != b.Signing {
		return ErrInvalid
	}
	var ns Namespace
	if json.Unmarshal([]byte(s.ns), &ns) != nil || r.Server != ns.Server || r.Principal != ns.Principal || r.Device != ns.Device {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		ref := Ref{Transition, r.TransitionID}
		old, e := s.get(tx, ref)
		if e != nil {
			return e
		}
		expected := AccountTransition{AccountID: accountID, Reset: r, Bundle: b}
		if recovery {
			expected.Recovery, expected.FromEpoch = true, c.AccountEpoch
		}
		if old.Revision > 0 {
			var prior AccountTransition
			if json.Unmarshal(old.Data, &prior) != nil {
				return ErrCorrupt
			}
			prior.Receipt = nil
			if !bytes.Equal(encode(prior), encode(expected)) {
				return ErrConflict
			}
			return nil
		}
		if r.NewEpoch != r.OldEpoch+1 || c.AccountID == accountID || c.SendingBlocked || (recovery && c.AccountEpoch > r.OldEpoch) || (!recovery && c.AccountEpoch != r.OldEpoch) {
			return ErrFenced
		}
		ar := Ref{Account, accountID}
		prior, e := s.get(tx, ar)
		if e != nil {
			return e
		}
		if prior.Revision != 0 {
			return ErrConflict
		}
		changes := map[Ref][]byte{ref: encode(expected), ar: account, Ref{Publication, r.TransitionID}: encode(PublicationRecord{accountID, encode(b)})}
		seenKeys := map[string]bool{}
		for id, o := range otks {
			key := Ref{Obligation, id}
			v, e := s.get(tx, key)
			if e != nil {
				return e
			}
			if v.Revision != 0 || o.Consumed || o.Closed || o.AccountID != accountID || o.AccountEpoch != r.NewEpoch || o.PublicationID != r.TransitionID {
				return ErrConflict
			}
			found := false
			for _, k := range b.OneTimeKeys {
				if k.ID == o.KeyID && k.Key == o.PublicKey {
					found = true
					break
				}
			}
			if !found || seenKeys[o.KeyID] {
				return ErrInvalid
			}
			seenKeys[o.KeyID] = true
			changes[key] = encode(o)
		}
		c.SendingBlocked = true
		return s.changes(tx, c, snap, changes)
	})
}

// PromoteAccount accepts only the independently authenticated exact receipt for
// a previously durable signed intent. It never promotes a server-supplied pin.
func (s *Store) PromoteAccount(snap Snapshot, id string, receipt olmdir.ResetReceipt) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		ref := Ref{Transition, id}
		v, e := s.get(tx, ref)
		if e != nil {
			return e
		}
		var t AccountTransition
		if v.Revision == 0 || json.Unmarshal(v.Data, &t) != nil {
			return ErrInvalid
		}
		r := t.Reset
		if receipt.Server != r.Server || receipt.Device != r.Device || receipt.TransitionID != r.TransitionID || receipt.AccountEpoch != r.NewEpoch || receipt.PublicationDigest != r.PublicationDigest || receipt.Digest != admission.Digest(encode(r)) {
			return ErrConflict
		}
		if t.Receipt != nil {
			if *t.Receipt != receipt {
				return ErrConflict
			}
			return nil
		}
		if t.Abandoned {
			return ErrFenced
		}
		from := r.OldEpoch
		if t.Recovery {
			from = t.FromEpoch
		}
		if c.AccountEpoch != from || !c.SendingBlocked {
			return ErrFenced
		}
		t.Receipt = &receipt
		c.AccountID = t.AccountID
		c.AccountEpoch = r.NewEpoch
		c.NextAttempt = 0
		c.SendingBlocked = false
		return s.changes(tx, c, snap, map[Ref][]byte{ref: encode(t)})
	})
}

// AbandonRecoveryTransition gives up a prepared, unpromoted RECOVERY intent
// that the server deterministically refused (for example a reset built from
// a stale or forged own_state): sending authority returns to its previous
// state and a later explicit recovery may build a new intent. An ordinary
// transition or one with a receipt can never be abandoned.
func (s *Store) AbandonRecoveryTransition(snap Snapshot, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		ref := Ref{Transition, id}
		v, e := s.get(tx, ref)
		if e != nil {
			return e
		}
		var t AccountTransition
		if v.Revision == 0 || json.Unmarshal(v.Data, &t) != nil || !t.Recovery || t.Receipt != nil {
			return ErrInvalid
		}
		if t.Abandoned {
			return nil
		}
		if c.AccountEpoch != t.FromEpoch || !c.SendingBlocked {
			return ErrFenced
		}
		t.Abandoned = true
		c.SendingBlocked = false
		return s.changes(tx, c, snap, map[Ref][]byte{ref: encode(t)})
	})
}

func (s *Store) AccountTransitionRequest(fence uint64, id string) (AccountTransition, error) {
	var out AccountTransition
	e := s.db.View(func(tx *bolt.Tx) error {
		if _, e := s.fence(tx, Snapshot{Namespace: s.ns, Fence: fence}); e != nil {
			return e
		}
		v, e := s.get(tx, Ref{Transition, id})
		if e != nil {
			return e
		}
		if v.Revision == 0 || json.Unmarshal(v.Data, &out) != nil {
			return ErrInvalid
		}
		return nil
	})
	return out, e
}

// Caller verifies the authenticated lifecycle response, then runs upstream
// retire_prekey for these exact public keys. The resulting candidate and closure
// evidence commit atomically; a timer or missing publication is not evidence.
func (s *Store) RetireUnsubmittedKeys(snap Snapshot, accountID string, account []byte, proofs map[string]KeyClosure) error {
	if len(account) == 0 || len(proofs) < 1 || len(proofs) > 100 {
		return ErrInvalid
	}
	var ns Namespace
	_ = json.Unmarshal([]byte(s.ns), &ns)
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		ar := Ref{Account, accountID}
		if _, ok := snap.Records[ar]; !ok {
			return ErrInvalid
		}
		changes := map[Ref][]byte{ar: account}
		for id, p := range proofs {
			ref := Ref{Obligation, id}
			v, e := s.get(tx, ref)
			if e != nil {
				return e
			}
			var o OTKObligation
			if v.Revision == 0 || json.Unmarshal(v.Data, &o) != nil {
				return ErrInvalid
			}
			if p.State != "closed" || p.Server != ns.Server || p.Device != ns.Device || p.AccountEpoch != o.AccountEpoch || p.KeyID != o.KeyID || o.AccountID != accountID || o.Consumed || o.PublicKey == "" || len(p.Proof) != 64 {
				return ErrConflict
			}
			proof := p
			p.Proof = ""
			if proof.Proof != admission.Digest(encode(p)) {
				return ErrConflict
			}
			p = proof
			if o.Closed {
				return ErrConflict
			}
			o.Closed = true
			o.ClosureProof = p.Proof
			changes[ref] = encode(o)
		}
		return s.changes(tx, c, snap, changes)
	})
}

// A verified peer transition retires old-epoch send sessions without deleting
// their delayed-receive material or weakening ordinary Replaces equality.
func (s *Store) RetirePeerSessions(snap Snapshot, trustID string, r olmdir.Reset, ids []string) error {
	if len(ids) < 1 || len(ids) > 160 {
		return ErrInvalid
	}
	var ns Namespace
	_ = json.Unmarshal([]byte(s.ns), &ns)
	if r.Server != ns.Server {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		if e = s.checkTrust(tx, snap, trustID); e != nil {
			return e
		}
		changes := map[Ref][]byte{}
		for _, id := range ids {
			ref := Ref{Session, id}
			v, e := s.get(tx, ref)
			if e != nil {
				return e
			}
			var state SessionState
			if v.Revision == 0 || json.Unmarshal(v.Data, &state) != nil {
				return ErrInvalid
			}
			if state.Context.PeerPrincipal != r.Principal || state.Context.PeerDevice != r.Device || state.Context.PeerEpoch != r.OldEpoch || state.Context.PeerIdentity != r.OldIdentity {
				return ErrConflict
			}
			state.Retired = true
			changes[ref] = encode(state)
		}
		return s.changes(tx, c, snap, changes)
	})
}
