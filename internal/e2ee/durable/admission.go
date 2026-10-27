package durable

import (
	"bytes"
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"noxa/internal/e2ee/admission"
)

const Logical = "logical"
const Transition = "transition"

type AttemptDescriptor struct {
	Manifest admission.Manifest `json:"manifest"`
	Parts    [][]byte           `json:"parts"`
}
type logicalRecord struct {
	AttemptID string
	State     string
}
type AttemptMetadata struct {
	Manifest admission.Manifest `json:"manifest"`
	Digest   string             `json:"digest"`
	Receipt  *admission.Receipt `json:"receipt,omitempty"`
}
type ReceiptAcknowledgment struct {
	AttemptID    string `json:"attempt_id"`
	AccountEpoch int64  `json:"account_epoch"`
	Digest       string `json:"digest"`
}

func AttemptID(m admission.Manifest) string { return fmt.Sprintf("%d:%s", m.AccountEpoch, m.AttemptID) }
func checkSendAuthority(c control, packets []SendCandidate, attempt *AttemptDescriptor) error {
	if c.SendingBlocked || c.AccountEpoch < 1 || c.AccountID == "" {
		return ErrFenced
	}
	if attempt != nil && attempt.Manifest.AccountEpoch != c.AccountEpoch {
		return ErrFenced
	}
	for _, p := range packets {
		if p.Context.LocalAccountID != c.AccountID || p.Context.LocalEpoch != c.AccountEpoch {
			return ErrFenced
		}
	}
	return nil
}
func (s *Store) CommitAttempt(snap Snapshot, m admission.Manifest, parts [][]byte, packets []SendCandidate, archive []byte) (SendResult, error) {
	copies, e := admission.Open(m, parts)
	if e != nil {
		return SendResult{}, ErrInvalid
	}
	var ns Namespace
	if json.Unmarshal([]byte(s.ns), &ns) != nil || ns.Server != m.Server || ns.Principal != m.Sender || ns.Device != m.Device || len(copies) != len(packets) {
		return SendResult{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range packets {
		var copy *admission.Copy
		for i := range copies {
			if copies[i].Device == p.Context.PeerDevice {
				copy = &copies[i]
				break
			}
		}
		if copy == nil || seen[copy.Device] || copy.Principal != p.Context.PeerPrincipal || copy.AccountEpoch != p.Context.PeerEpoch || !bytes.Equal(copy.Envelope, p.Envelope) {
			return SendResult{}, ErrInvalid
		}
		if admission.CheckEnvelope(m, *copy) != nil {
			return SendResult{}, ErrInvalid
		}
		matched := false
		for _, target := range m.Targets {
			if target.Device == copy.Device {
				claimID := ""
				if p.Claim != nil {
					claimID = p.Claim.ClaimID
				}
				// The wire copy names exactly this packet's session and claim, so an
				// accepted receipt establishes the session that was admitted.
				matched = p.Context.PeerIdentity == target.Identity && p.Context.Scope == DevicePairScope &&
					p.SessionID == SessionRef(copy.SessionID) && copy.ClaimID == claimID
			}
		}
		if !matched {
			return SendResult{}, ErrInvalid
		}
		seen[copy.Device] = true
	}
	return s.commitSend(snap, AttemptID(m), packets, archive, &AttemptDescriptor{Manifest: m, Parts: parts})
}
func (s *Store) prepareAttempt(tx *bolt.Tx, c *control, snap Snapshot, d *AttemptDescriptor, changes map[Ref][]byte) error {
	m := d.Manifest
	seq, e := admission.AttemptSequence(m.AttemptID)
	if e != nil {
		return ErrInvalid
	}
	if seq != c.NextAttempt+1 {
		return ErrConflict
	}
	ref := Ref{Logical, m.LogicalID}
	if _, ok := snap.Records[ref]; !ok {
		return ErrInvalid
	}
	r, e := s.get(tx, ref)
	if e != nil {
		return e
	}
	if r.Revision > 0 {
		var old logicalRecord
		if json.Unmarshal(r.Data, &old) != nil {
			return ErrCorrupt
		}
		if old.State != "rejected" && old.State != "aborted" {
			return ErrConflict
		}
	}
	changes[ref] = encode(logicalRecord{AttemptID(m), "pending"})
	c.NextAttempt = seq
	return nil
}

// Metadata-only resolution remains available after peer/account changes.
func (s *Store) AttemptStatusRequest(fence uint64, id string) (AttemptMetadata, error) {
	var out AttemptMetadata
	e := s.db.View(func(tx *bolt.Tx) error {
		if _, e := s.fence(tx, Snapshot{Namespace: s.ns, Fence: fence}); e != nil {
			return e
		}
		r, e := s.get(tx, Ref{Outbox, id})
		if e != nil {
			return e
		}
		var b sentBatch
		if r.Revision == 0 || json.Unmarshal(r.Data, &b) != nil || b.Attempt == nil {
			return ErrInvalid
		}
		mb, _ := b.Attempt.Manifest.Bytes()
		out = AttemptMetadata{b.Attempt.Manifest, admission.Digest(mb), b.Receipt}
		return nil
	})
	return out, e
}
func (s *Store) ApplyAttemptResult(snap Snapshot, id string, result admission.Receipt) error {
	if result.State != "accepted" && result.State != "rejected" && result.State != "aborted" {
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
		ref := Ref{Outbox, id}
		r, e := s.get(tx, ref)
		if e != nil {
			return e
		}
		var b sentBatch
		if r.Revision == 0 || json.Unmarshal(r.Data, &b) != nil || b.Attempt == nil {
			return ErrInvalid
		}
		m := b.Attempt.Manifest
		mb, _ := m.Bytes()
		if result.Server != m.Server || result.AccountEpoch != m.AccountEpoch || result.AttemptID != m.AttemptID || result.LogicalID != m.LogicalID || result.Digest != admission.Digest(mb) || (result.State == "accepted" && result.MessageID == "") {
			return ErrConflict
		}
		if b.Receipt != nil {
			if *b.Receipt != result {
				return ErrConflict
			}
			return nil
		}
		lr := Ref{Logical, m.LogicalID}
		old, e := s.get(tx, lr)
		if e != nil {
			return e
		}
		var logical logicalRecord
		if old.Revision == 0 || json.Unmarshal(old.Data, &logical) != nil || logical.AttemptID != id {
			return ErrConflict
		}
		b.Receipt = &result
		b.Status = result.State
		logical.State = result.State
		changes := map[Ref][]byte{ref: encode(b), lr: encode(logical)}
		if result.State == "accepted" {
			// The server now has the session rows: first-contact sessions of
			// this attempt are established and stop carrying their claim.
			// Read inside this transaction, so they join the CAS set.
			records := make(map[Ref]Record, len(snap.Records)+len(b.Packets))
			for k, v := range snap.Records {
				records[k] = v
			}
			for _, p := range b.Packets {
				sr := Ref{Session, p.SessionID}
				cur, e := s.get(tx, sr)
				if e != nil {
					return e
				}
				var st SessionState
				if cur.Revision == 0 || json.Unmarshal(cur.Data, &st) != nil {
					return ErrCorrupt
				}
				if st.Pending == nil {
					continue
				}
				st.Pending = nil
				records[sr] = cur
				changes[sr] = encode(st)
			}
			snap.Records = records
		}
		return s.changes(tx, c, snap, changes)
	})
}
func (s *Store) AttemptRetirementRequest(fence uint64, id string) (ReceiptAcknowledgment, error) {
	m, e := s.AttemptStatusRequest(fence, id)
	if e != nil {
		return ReceiptAcknowledgment{}, e
	}
	if m.Receipt == nil {
		return ReceiptAcknowledgment{}, ErrInvalid
	}
	return ReceiptAcknowledgment{m.Manifest.AttemptID, m.Manifest.AccountEpoch, m.Receipt.Digest}, nil
}

// Return only committed wire parts, never the local readable archive.
func (s *Store) AttemptWire(fence uint64, id string) (AttemptDescriptor, error) {
	r, e := s.RetrySend(fence, id)
	if e != nil {
		return AttemptDescriptor{}, e
	}
	b, e := r.Operation()
	if e != nil || b.Attempt == nil || b.Status != "pending" {
		return AttemptDescriptor{}, ErrInvalid
	}
	return *b.Attempt, nil
}
