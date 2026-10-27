// Package durable is the local transaction boundary for candidate Olm state.
// It deliberately has no transport callbacks. Only successfully committed return
// values may be published; see README.md for trust and backup requirements.
package durable

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"noxa/internal/e2ee/admission"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrConflict    = errors.New("ratchet store: stale revision")
	ErrFenced      = errors.New("ratchet store: identity inactive or replaced")
	ErrRevoked     = errors.New("ratchet store: peer revoked")
	ErrCorrupt     = errors.New("ratchet store: unreadable protected state")
	ErrExhausted   = errors.New("ratchet store: capacity exhausted")
	ErrInvalid     = errors.New("ratchet store: invalid operation")
	ErrInUse       = errors.New("ratchet store: in use by another process")
	ErrUnavailable = errors.New("ratchet store: local key protection unavailable")
)

const (
	Account           = "account"
	Session           = "session"
	Trust             = "trust"
	Outbox            = "outbox"
	Inbox             = "inbox"
	Publication       = "publication"
	Claim             = "claim"
	Obligation        = "obligation"
	MaxFanout         = 160 // default 16 members x 10 devices, including local copies
	MaxReads          = 512
	MaxRecordBytes    = 2 << 20
	MaxBatchBytes     = 32 << 20
	MaxNamespaceBytes = 128 << 20
	MaxRecords        = 100000
	maxRevision       = uint64(9007199254740991)
)

type Namespace struct{ Server, Principal, Device string }
type Ref struct{ Kind, ID string }
type Record struct {
	Revision uint64 `json:"revision"`
	Data     []byte `json:"data"`
}
type Snapshot struct {
	Namespace string
	Fence     uint64
	Records   map[Ref]Record
}
type Protection struct {
	Seal func([]byte) ([]byte, error)
	Open func([]byte) ([]byte, error)
}
type Store struct {
	db    *bolt.DB
	ns    string
	key   []byte
	aead  cipher.AEAD
	keyMu sync.RWMutex
}
type control struct {
	Fence          uint64
	Active         bool
	Count, Bytes   int
	Outstanding    int
	AccountID      string
	AccountEpoch   int64
	SendingBlocked bool
	NextAttempt    int64
}

var bucket = []byte("protected-v1")
var keyBucket = []byte("key-v1")
var controlRef = Ref{"control", "identity"}

func validID(s string) bool {
	return s != "" && len(s) <= 512 && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validRef(r Ref) bool {
	switch r.Kind {
	case Account, Session, Trust, Outbox, Inbox, Publication, Claim, Obligation, Transition, Logical:
		return validID(r.ID)
	}
	return false
}
func encode(v any) []byte { b, _ := json.Marshal(v); return b }
func keyOf(r Ref) []byte  { return encode(r) }
func (s *Store) aad(r Ref, rev uint64) []byte {
	return encode([]any{"noxa-local-ratchet-v1", s.ns, r.Kind, r.ID, rev})
}
func cipherFor(key []byte) (cipher.AEAD, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}

// IdentityProtection is an explicit fallback, NOT an OS vault. Its security is
// only as strong as the caller's existing identity-secret protection. No secret
// or unprotected random master key is written by this adapter.
func IdentityProtection(identitySecret []byte) Protection {
	if len(identitySecret) < 32 {
		return Protection{}
	}
	h := sha256.New()
	h.Write([]byte("noxa-local-ratchet-master-wrap-v1\x00"))
	h.Write(identitySecret)
	a, _ := cipherFor(h.Sum(nil))
	aad := []byte("noxa-local-ratchet-master-v1")
	return Protection{
		Seal: func(p []byte) ([]byte, error) {
			n := make([]byte, a.NonceSize())
			if _, e := rand.Read(n); e != nil {
				return nil, e
			}
			return a.Seal(n, n, p, aad), nil
		},
		Open: func(p []byte) ([]byte, error) {
			if len(p) < a.NonceSize() {
				return nil, ErrCorrupt
			}
			return a.Open(nil, p[:a.NonceSize()], p[a.NonceSize():], aad)
		},
	}
}

// Open exclusively locks a hashed namespace file before reading/creating its
// wrapped master key. Timeout is bounded; sync-on-commit is never disabled.
func Open(dir string, ns Namespace, protection Protection, timeout time.Duration) (*Store, error) {
	if !validID(ns.Server) || !validID(ns.Principal) || !validID(ns.Device) {
		return nil, ErrInvalid
	}
	if protection.Seal == nil || protection.Open == nil {
		return nil, ErrUnavailable
	}
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 500 * time.Millisecond
	}
	namespace := string(encode(ns))
	hash := sha256.Sum256([]byte(namespace))
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	file := filepath.Join(dir, hex.EncodeToString(hash[:])+".db")
	db, e := bolt.Open(file, 0600, &bolt.Options{Timeout: timeout})
	if errors.Is(e, bolt.ErrTimeout) {
		return nil, ErrInUse
	}
	if e != nil {
		return nil, e
	}
	s := &Store{db: db, ns: namespace}
	e = db.Update(func(tx *bolt.Tx) error {
		if err := tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if !bytes.Equal(name, keyBucket) && !bytes.Equal(name, bucket) {
				return ErrCorrupt
			}
			return nil
		}); err != nil {
			return err
		}
		kb := tx.Bucket(keyBucket)
		created := kb == nil
		b := tx.Bucket(bucket)
		if kb == nil {
			// bbolt creates its file before our first transaction. A failed vault
			// call or interrupted first commit may leave a valid bucketless DB.
			// Any bucket proves this is not an uninitialized database.
			if err := tx.ForEach(func([]byte, *bolt.Bucket) error { return ErrCorrupt }); err != nil {
				return err
			}
			var err error
			kb, err = tx.CreateBucket(keyBucket)
			if err != nil {
				return err
			}
			s.key = make([]byte, 32)
			if _, err = rand.Read(s.key); err != nil {
				return err
			}
			wrapped, err := protection.Seal(s.key)
			if err != nil {
				return err
			}
			if err = kb.Put([]byte("master"), wrapped); err != nil {
				return err
			}
			if _, err = tx.CreateBucket(bucket); err != nil {
				return err
			}
		} else {
			if b == nil {
				return ErrCorrupt
			}
			var err error
			s.key, err = protection.Open(kb.Get([]byte("master")))
			if err != nil || len(s.key) != 32 {
				return ErrCorrupt
			}
		}
		var err error
		s.aead, err = cipherFor(s.key)
		if err != nil {
			return err
		}
		rec, err := s.get(tx, controlRef)
		if err != nil {
			return err
		}
		if rec.Revision == 0 {
			if !created {
				return ErrCorrupt
			}
			// Only a truly empty new DB may initialize a control record.
			if tx.Bucket(bucket).Stats().KeyN != 0 {
				return ErrCorrupt
			}
			return s.put(tx, controlRef, Record{1, encode(control{Fence: 1, Active: true, AccountID: "local", AccountEpoch: 1})})
		}
		_, err = s.control(tx)
		return err
	})
	if e != nil {
		clear(s.key)
		_ = db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { e := s.db.Close(); clear(s.key); return e }

// CoreKey returns a copy for the pure core's protected snapshots; never transport it.
func (s *Store) CoreKey() []byte {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return append([]byte(nil), s.key...)
}
func (s *Store) get(tx *bolt.Tx, r Ref) (Record, error) {
	raw := tx.Bucket(bucket).Get(keyOf(r))
	if raw == nil {
		return Record{}, nil
	}
	if len(raw) < 8+s.aead.NonceSize()+s.aead.Overhead() {
		return Record{}, ErrCorrupt
	}
	rev := binary.BigEndian.Uint64(raw[:8])
	if rev == 0 || rev > maxRevision {
		return Record{}, ErrCorrupt
	}
	p, e := s.aead.Open(nil, raw[8:8+s.aead.NonceSize()], raw[8+s.aead.NonceSize():], s.aad(r, rev))
	if e != nil {
		return Record{}, ErrCorrupt
	}
	return Record{rev, p}, nil
}
func (s *Store) put(tx *bolt.Tx, r Ref, v Record) error {
	if v.Revision == 0 || v.Revision > maxRevision {
		return ErrExhausted
	}
	n := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return e
	}
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, v.Revision)
	raw = append(raw, n...)
	raw = s.aead.Seal(raw, n, v.Data, s.aad(r, v.Revision))
	return tx.Bucket(bucket).Put(keyOf(r), raw)
}
func (s *Store) control(tx *bolt.Tx) (control, error) {
	r, e := s.get(tx, controlRef)
	if e != nil {
		return control{}, e
	}
	var c control
	if r.Revision == 0 || json.Unmarshal(r.Data, &c) != nil || c.Fence == 0 || c.Fence > maxRevision || c.Count < 0 || c.Bytes < 0 {
		return c, ErrCorrupt
	}
	return c, nil
}
func (s *Store) Read(refs ...Ref) (Snapshot, error) {
	out := Snapshot{Namespace: s.ns, Records: make(map[Ref]Record)}
	if len(refs) > MaxReads {
		return out, ErrExhausted
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		c, e := s.control(tx)
		if e != nil {
			return e
		}
		if !c.Active {
			return ErrFenced
		}
		out.Fence = c.Fence
		total := 0
		for _, r := range refs {
			if !validRef(r) {
				return ErrInvalid
			}
			v, e := s.get(tx, r)
			if e != nil {
				return e
			}
			total += len(v.Data)
			if total > MaxBatchBytes {
				return ErrExhausted
			}
			out.Records[r] = v
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}
func (s *Store) fence(tx *bolt.Tx, snap Snapshot) (control, error) {
	if snap.Namespace != s.ns {
		return control{}, ErrInvalid
	}
	c, e := s.control(tx)
	if e == nil && (!c.Active || c.Fence != snap.Fence) {
		e = ErrFenced
	}
	return c, e
}
func (s *Store) cas(tx *bolt.Tx, snap Snapshot) error {
	if len(snap.Records) > MaxReads {
		return ErrExhausted
	}
	for r, want := range snap.Records {
		if !validRef(r) {
			return ErrInvalid
		}
		got, e := s.get(tx, r)
		if e != nil {
			return e
		}
		if got.Revision != want.Revision {
			return ErrConflict
		}
	}
	return nil
}
func (s *Store) changes(tx *bolt.Tx, c control, snap Snapshot, changes map[Ref][]byte) error {
	total := 0
	for r, data := range changes {
		if !validRef(r) || len(data) == 0 {
			return ErrInvalid
		}
		limit := MaxRecordBytes
		if r.Kind == Outbox {
			limit = 24 << 20
		}
		if len(data) > limit {
			return ErrExhausted
		}
		total += len(data)
		if total > MaxBatchBytes {
			return ErrExhausted
		}
		old, e := s.get(tx, r)
		if e != nil {
			return e
		}
		if old.Revision == 0 {
			c.Count++
		}
		c.Bytes += len(data) - len(old.Data)
		if r.Kind == Obligation {
			var after, before OTKObligation
			if json.Unmarshal(data, &after) != nil {
				return ErrInvalid
			}
			if old.Revision > 0 && json.Unmarshal(old.Data, &before) != nil {
				return ErrCorrupt
			}
			if old.Revision == 0 && !after.Consumed && !after.Closed {
				c.Outstanding++
			}
			if old.Revision > 0 && !before.Consumed && !before.Closed && (after.Consumed || after.Closed) {
				c.Outstanding--
			}
		}
		if _, ok := snap.Records[r]; !ok && old.Revision != 0 {
			return ErrConflict
		}
		if e = s.put(tx, r, Record{old.Revision + 1, data}); e != nil {
			return e
		}
	}
	if total > MaxBatchBytes || c.Count > MaxRecords || c.Bytes > MaxNamespaceBytes || c.Outstanding > 100 {
		return ErrExhausted
	}
	old, e := s.get(tx, controlRef)
	if e != nil {
		return e
	}
	return s.put(tx, controlRef, Record{old.Revision + 1, encode(c)})
}

type trustRecord struct {
	Revoked bool   `json:"revoked"`
	Value   []byte `json:"value"`
}

func (s *Store) checkTrust(tx *bolt.Tx, snap Snapshot, id string) error {
	r := Ref{Trust, id}
	if _, ok := snap.Records[r]; !ok {
		return ErrInvalid
	}
	v, e := s.get(tx, r)
	if e != nil {
		return e
	}
	var tr trustRecord
	if v.Revision == 0 || json.Unmarshal(v.Data, &tr) != nil {
		return ErrInvalid
	}
	if tr.Revoked {
		return ErrRevoked
	}
	return nil
}

// SetTrust is called only after the existing trust verifier/user approval. Every
// update advances the CAS revision. Revocation for this device ID is permanent.
func (s *Store) SetTrust(snap Snapshot, id string, value []byte, revoked bool) error {
	r := Ref{Trust, id}
	if _, ok := snap.Records[r]; !ok {
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
		old, e := s.get(tx, r)
		if e != nil {
			return e
		}
		var tr trustRecord
		if old.Revision > 0 {
			if json.Unmarshal(old.Data, &tr) != nil {
				return ErrCorrupt
			}
			if tr.Revoked && !revoked {
				return ErrRevoked
			}
		}
		return s.changes(tx, c, snap, map[Ref][]byte{r: encode(trustRecord{revoked, value})})
	})
}

// DevicePairScope is the only session scope: one Olm session per local
// account/device pair, reused by every direct and group conversation. The
// core authenticates each message's conversation binding separately. Records
// written with an earlier per-conversation context (pre-activation test data)
// fail validation and context equality, so they are never mixed or reused.
const DevicePairScope = "device-pair"

type SessionContext struct {
	LocalAccountID string `json:"local_account_id"`
	LocalEpoch     int64  `json:"local_epoch"`
	PeerEpoch      int64  `json:"peer_epoch"`
	PeerPrincipal  string `json:"peer_principal"`
	PeerDevice     string `json:"peer_device"`
	PeerIdentity   string `json:"peer_identity"`
	Scope          string `json:"scope"`
	Protocol       string `json:"protocol"`
}
// SessionClaim is the reserved directory claim that created an outbound
// first-contact session. While a session is Pending, the server has no session
// row yet: every attempt using it must carry this claim_id (valid through the
// attempt deadline). An accepted attempt, or any authenticated receive on the
// session, clears Pending. Receive-created and legacy records have no Pending.
type SessionClaim struct {
	ClaimID    string `json:"claim_id"`
	ClaimUntil int64  `json:"claim_until"`
}
type SessionState struct {
	Snapshot  []byte         `json:"snapshot"`
	Context   SessionContext `json:"context"`
	CreatedAt int64          `json:"created_at"`
	Retired   bool           `json:"retired"`
	Pending   *SessionClaim  `json:"pending,omitempty"`
}

func validContext(c SessionContext) bool {
	return validID(c.LocalAccountID) && c.LocalEpoch > 0 && c.PeerEpoch > 0 && validID(c.PeerPrincipal) && validID(c.PeerDevice) && validID(c.PeerIdentity) && c.Scope == DevicePairScope && c.Protocol == "noxa-olm-v1"
}
func (s *Store) sessionValue(tx *bolt.Tx, id string, state []byte, context SessionContext, receiving bool, claim *SessionClaim) ([]byte, error) {
	if len(state) == 0 || !validContext(context) || (claim != nil && (receiving || !validID(claim.ClaimID) || claim.ClaimUntil <= 0)) {
		return nil, ErrInvalid
	}
	old, e := s.get(tx, Ref{Session, id})
	if e != nil {
		return nil, e
	}
	v := SessionState{Snapshot: state, Context: context, CreatedAt: time.Now().Unix(), Pending: claim}
	if old.Revision > 0 {
		var prev SessionState
		if json.Unmarshal(old.Data, &prev) != nil {
			return nil, ErrCorrupt
		}
		if prev.Context != context {
			return nil, ErrConflict
		}
		v.CreatedAt = prev.CreatedAt
		v.Retired = prev.Retired
		if prev.Retired && !receiving {
			return nil, ErrConflict
		}
		// A pending session keeps its original claim; an established one
		// cannot acquire a claim. Receiving on it proves admission.
		if !receiving && prev.Pending != nil && claim == nil {
			return nil, ErrConflict
		}
		if claim != nil && (prev.Pending == nil || *prev.Pending != *claim) {
			return nil, ErrConflict
		}
		v.Pending = prev.Pending
		if receiving {
			v.Pending = nil
		}
	}
	return encode(v), nil
}

type SendCandidate struct {
	SessionID, TrustID string
	Replaces           string
	State, Envelope    []byte
	Context            SessionContext
	TrustRevision      uint64
	// Claim: for a new first-contact session, the reserved claim it was
	// created with; for a pending session, exactly its recorded claim.
	Claim *SessionClaim `json:",omitempty"`
}
type SendResult struct {
	Data   []byte
	Reused bool
}

type sentBatch struct {
	Attempt *AttemptDescriptor `json:"attempt,omitempty"`
	Receipt *admission.Receipt `json:"receipt,omitempty"`
	Packets []SendCandidate    `json:"packets"`
	Status  string             `json:"status"`
	Archive []byte             `json:"archive"`
}

func (s *Store) CommitSend(snap Snapshot, id string, packets []SendCandidate, archive []byte) (SendResult, error) {
	return s.commitSend(snap, id, packets, archive, nil)
}
func (s *Store) commitSend(snap Snapshot, id string, packets []SendCandidate, archive []byte, attempt *AttemptDescriptor) (SendResult, error) {
	if len(archive) > MaxRecordBytes {
		return SendResult{}, ErrExhausted
	}
	if !validID(id) || (len(packets) == 0 && attempt == nil) || len(archive) == 0 {
		return SendResult{}, ErrInvalid
	}
	if len(packets) > MaxFanout {
		return SendResult{}, ErrExhausted
	}
	ref := Ref{Outbox, id}
	var out []byte
	reused := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		// Trust is checked even on exact retries, against current durable revocation.
		old, e := s.get(tx, ref)
		if e != nil {
			return e
		}
		if old.Revision > 0 {
			var saved sentBatch
			if json.Unmarshal(old.Data, &saved) != nil {
				return ErrCorrupt
			}
			if e = checkSendAuthority(c, saved.Packets, saved.Attempt); e != nil {
				return e
			}
			for _, p := range saved.Packets {
				if e = s.checkTrust(tx, snap, p.TrustID); e != nil {
					return e
				}
				current, e := s.get(tx, Ref{Trust, p.TrustID})
				if e != nil {
					return e
				}
				if current.Revision != p.TrustRevision {
					return ErrConflict
				}
			}
			out = old.Data
			reused = true
			return nil
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		changes := map[Ref][]byte{}
		saved := sentBatch{Status: "pending", Archive: archive, Attempt: attempt}
		if e = checkSendAuthority(c, packets, attempt); e != nil {
			return e
		}
		if attempt != nil {
			if e = s.prepareAttempt(tx, &c, snap, attempt, changes); e != nil {
				return e
			}
		}
		for _, p := range packets {
			r := Ref{Session, p.SessionID}
			if _, ok := snap.Records[r]; !ok {
				return ErrInvalid
			}
			if _, ok := changes[r]; ok {
				return ErrInvalid
			}
			if len(p.Envelope) > MaxRecordBytes || len(p.State) > MaxRecordBytes {
				return ErrExhausted
			}
			if len(p.Envelope) == 0 {
				return ErrInvalid
			}
			if e = s.checkTrust(tx, snap, p.TrustID); e != nil {
				return e
			}
			state, e := s.sessionValue(tx, p.SessionID, p.State, p.Context, false, p.Claim)
			if e != nil {
				return e
			}
			changes[r] = state
			if p.Replaces != "" {
				oldRef := Ref{Session, p.Replaces}
				old, ok := snap.Records[oldRef]
				if !ok || old.Revision == 0 || snap.Records[r].Revision != 0 || p.Replaces == p.SessionID {
					return ErrInvalid
				}
				if _, exists := changes[oldRef]; exists {
					return ErrInvalid
				}
				var previous SessionState
				if json.Unmarshal(old.Data, &previous) != nil {
					return ErrCorrupt
				}
				if previous.Context != p.Context || previous.Retired {
					return ErrConflict
				}
				previous.Retired = true
				changes[oldRef] = encode(previous)
			}
			// Outbox contains exact envelopes and references, never redundant secret state.
			saved.Packets = append(saved.Packets, SendCandidate{SessionID: p.SessionID, TrustID: p.TrustID, Envelope: p.Envelope, TrustRevision: snap.Records[Ref{Trust, p.TrustID}].Revision, Context: p.Context})
		}
		out = encode(saved)
		changes[ref] = out
		return s.changes(tx, c, snap, changes)
	})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{out, reused}, nil
}

// Ack changes delivery state only. Session snapshots can never be rewound here.
func (s *Store) Ack(snap Snapshot, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		r := Ref{Outbox, id}
		v, e := s.get(tx, r)
		if e != nil {
			return e
		}
		var b sentBatch
		if v.Revision == 0 || json.Unmarshal(v.Data, &b) != nil {
			return ErrInvalid
		}
		if b.Attempt != nil {
			return ErrInvalid
		}
		if b.Status == "sent" {
			return nil
		}
		b.Status = "sent"
		return s.changes(tx, c, snap, map[Ref][]byte{r: encode(b)})
	})
}

type ReceiveCandidate struct {
	AccountID                           string
	SessionID, TrustID                  string
	Session, Account, Archive, Envelope []byte
	ConsumedOTK                         string
	Context                             SessionContext
}
type received struct {
	Digest  string `json:"digest"`
	Archive []byte `json:"archive"`
}

func (s *Store) CommitReceive(snap Snapshot, id string, p ReceiveCandidate) ([]byte, error) {
	if (len(p.Account) > 0) != (p.ConsumedOTK != "") {
		return nil, ErrInvalid
	}
	if len(p.Envelope) > MaxRecordBytes || len(p.Archive) > MaxRecordBytes {
		return nil, ErrExhausted
	}
	if !validID(id) || len(p.Envelope) == 0 || len(p.Archive) == 0 {
		return nil, ErrInvalid
	}
	r := Ref{Inbox, id}
	hash := sha256.Sum256(p.Envelope)
	digest := hex.EncodeToString(hash[:])
	var archive []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		old, e := s.get(tx, r)
		if e != nil {
			return e
		}
		if old.Revision > 0 {
			var rec received
			if json.Unmarshal(old.Data, &rec) != nil {
				return ErrCorrupt
			}
			if rec.Digest != digest {
				return ErrConflict
			}
			archive = rec.Archive
			return nil
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		if e = s.checkTrust(tx, snap, p.TrustID); e != nil {
			return e
		}
		sr := Ref{Session, p.SessionID}
		if _, ok := snap.Records[sr]; !ok {
			return ErrInvalid
		}
		state, e := s.sessionValue(tx, p.SessionID, p.Session, p.Context, true, nil)
		if e != nil {
			return e
		}
		changes := map[Ref][]byte{sr: state, r: encode(received{digest, p.Archive})}
		if len(p.Account) > 0 {
			accountID := p.AccountID
			if accountID == "" {
				accountID = "local"
			}
			ar := Ref{Account, accountID}
			if _, ok := snap.Records[ar]; !ok {
				return ErrInvalid
			}
			changes[ar] = p.Account
		}
		if p.ConsumedOTK != "" {
			or := Ref{Obligation, p.ConsumedOTK}
			v, ok := snap.Records[or]
			if !ok || v.Revision == 0 || len(p.Account) == 0 {
				return ErrInvalid
			}
			var o OTKObligation
			if json.Unmarshal(v.Data, &o) != nil {
				return ErrCorrupt
			}
			accountID := p.AccountID
			if accountID == "" {
				accountID = "local"
			}
			if o.AccountID != accountID || o.Closed {
				return ErrConflict
			}
			o.Consumed = true
			changes[or] = encode(o)
		}
		if e = s.changes(tx, c, snap, changes); e != nil {
			return e
		}
		archive = append([]byte(nil), p.Archive...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return archive, nil
}

// Logout destroys active state and archives by explicit account logout, never
// TTL, and advances a persistent fence. No API reactivates this namespace: a new
// device identity must use a new namespace, old backups are archive-only.
func (s *Store) Logout(fence uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.control(tx)
		if e != nil {
			return e
		}
		if c.Fence != fence {
			return ErrFenced
		}
		if c.Fence == maxRevision {
			return ErrExhausted
		}
		if e = tx.DeleteBucket(bucket); e != nil {
			return e
		}
		if _, e = tx.CreateBucket(bucket); e != nil {
			return e
		}
		return s.put(tx, controlRef, Record{1, encode(control{Fence: c.Fence + 1, Active: false})})
	})
}

// OTKObligation never expires a private key. Publication expiry permits no new
// claims, but an undelivered first message can still depend on it indefinitely.
type OTKObligation struct {
	AccountID     string `json:"account_id"`
	PublicationID string `json:"publication_id"`
	ExpiresAt     int64  `json:"expires_at"`
	Consumed      bool   `json:"consumed"`
	Closed        bool   `json:"closed"`
	ClosureProof  string `json:"closure_proof,omitempty"`
	AccountEpoch  int64  `json:"account_epoch"`
	KeyID         string `json:"key_id"`
	PublicKey     string `json:"public_key"`
}

func (s *Store) CommitPublication(snap Snapshot, id string, account, signed []byte, otks map[string]OTKObligation) ([]byte, error) {
	return s.CommitPublicationForAccount(snap, "local", id, account, signed, otks)
}

// CommitPublicationForAccount supports explicit epoch IDs under the shared device fence.
// It does not authorize or perform an account reset.
type PublicationRecord struct {
	AccountID string `json:"account_id"`
	Payload   []byte `json:"payload"`
}

func (s *Store) CommitPublicationForAccount(snap Snapshot, accountID, id string, account, signed []byte, otks map[string]OTKObligation) ([]byte, error) {
	if !validID(accountID) || !validID(id) || len(account) == 0 || len(signed) == 0 {
		return nil, ErrInvalid
	}
	if len(otks) == 0 || len(otks) > 100 {
		return nil, ErrExhausted
	}
	r := Ref{Publication, id}
	var out []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if c.SendingBlocked || accountID != c.AccountID {
			return ErrFenced
		}
		old, e := s.get(tx, r)
		if e != nil {
			return e
		}
		if old.Revision > 0 {
			var publication PublicationRecord
			if json.Unmarshal(old.Data, &publication) != nil {
				return ErrCorrupt
			}
			if publication.AccountID != accountID {
				return ErrConflict
			}
			out = publication.Payload
			return nil
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		ar := Ref{Account, accountID}
		if _, ok := snap.Records[ar]; !ok {
			return ErrInvalid
		}
		if c.Outstanding+len(otks) > 100 {
			return ErrExhausted
		}
		changes := map[Ref][]byte{ar: account, r: encode(PublicationRecord{accountID, signed})}
		for id, o := range otks {
			ref := Ref{Obligation, id}
			v, e := s.get(tx, ref)
			if e != nil {
				return e
			}
			if v.Revision != 0 || o.PublicationID != r.ID || o.Consumed || o.Closed {
				return ErrConflict
			}
			if o.AccountID != "" && o.AccountID != accountID {
				return ErrInvalid
			}
			o.AccountID = accountID
			if o.AccountEpoch != 0 && o.AccountEpoch != c.AccountEpoch {
				return ErrConflict
			}
			o.AccountEpoch = c.AccountEpoch
			if o.KeyID == "" {
				o.KeyID = id
			}
			changes[ref] = encode(o)
		}
		if e = s.changes(tx, c, snap, changes); e != nil {
			return e
		}
		out = append([]byte(nil), signed...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReserveClaim freezes the exact signed-directory request before transport.
// The fixed claim deadline is part of request bytes, never a private-key TTL.
type claimRecord struct {
	AccountID     string
	AccountEpoch  int64
	TrustID       string
	TrustRevision uint64
	Request       []byte
}

func (s *Store) ReserveClaim(snap Snapshot, id, trustID string, request []byte) ([]byte, error) {
	if !validID(id) || !validID(trustID) || len(request) == 0 {
		return nil, ErrInvalid
	}
	if len(request) > MaxRecordBytes {
		return nil, ErrExhausted
	}
	var out []byte
	r := Ref{Claim, id}
	err := s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, snap)
		if e != nil {
			return e
		}
		if c.SendingBlocked {
			return ErrFenced
		}
		if e = s.checkTrust(tx, snap, trustID); e != nil {
			return e
		}
		old, e := s.get(tx, r)
		if e != nil {
			return e
		}
		if old.Revision > 0 {
			var saved claimRecord
			if json.Unmarshal(old.Data, &saved) != nil {
				return ErrCorrupt
			}
			current, e := s.get(tx, Ref{Trust, saved.TrustID})
			if e != nil {
				return e
			}
			if saved.AccountID != c.AccountID || saved.AccountEpoch != c.AccountEpoch {
				return ErrFenced
			}
			if saved.TrustID != trustID || current.Revision != saved.TrustRevision {
				return ErrConflict
			}
			out = saved.Request
			return nil
		}
		if e = s.cas(tx, snap); e != nil {
			return e
		}
		if e = s.changes(tx, c, snap, map[Ref][]byte{r: encode(claimRecord{c.AccountID, c.AccountEpoch, trustID, snap.Records[Ref{Trust, trustID}].Revision, request})}); e != nil {
			return e
		}
		out = append([]byte(nil), request...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Store) String() string {
	return fmt.Sprintf("durable ratchet store (%s)", filepath.Base(s.db.Path()))
}

// PendingRefs provides bounded recovery enumeration. Values still need Read and
// operation-specific retry validation; enumeration never authorizes transport.
func (s *Store) PendingRefs(kind, after string, limit int) ([]Ref, error) {
	if !validRef(Ref{kind, "probe"}) || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	var refs []Ref
	err := s.db.View(func(tx *bolt.Tx) error {
		c, e := s.control(tx)
		if e != nil {
			return e
		}
		if !c.Active {
			return ErrFenced
		}
		prefix := []byte(`{"Kind":"` + kind + `","ID":`)
		cursor := tx.Bucket(bucket).Cursor()
		start := prefix
		if after != "" {
			start = keyOf(Ref{kind, after})
		}
		for k, _ := cursor.Seek(start); k != nil && bytes.HasPrefix(k, prefix) && len(refs) < limit; k, _ = cursor.Next() {
			var r Ref
			if json.Unmarshal(k, &r) != nil {
				return ErrCorrupt
			}
			if r.ID == after {
				continue
			}
			refs = append(refs, r)
		}
		return nil
	})
	return refs, err
}

// RetrySend revalidates identity and current peer trust under the transaction
// lock. The returned operation includes its original readable archive and status.
func (s *Store) RetrySend(fence uint64, id string) (SendResult, error) {
	var out SendResult
	err := s.db.View(func(tx *bolt.Tx) error {
		c, e := s.fence(tx, Snapshot{Fence: fence, Namespace: s.ns})
		if e != nil {
			return e
		}
		v, e := s.get(tx, Ref{Outbox, id})
		if e != nil {
			return e
		}
		if v.Revision == 0 {
			return ErrInvalid
		}
		var saved sentBatch
		if json.Unmarshal(v.Data, &saved) != nil {
			return ErrCorrupt
		}
		if e = checkSendAuthority(c, saved.Packets, saved.Attempt); e != nil {
			return e
		}
		if saved.Attempt != nil && saved.Status != "pending" {
			return ErrConflict
		}
		for _, p := range saved.Packets {
			r, e := s.get(tx, Ref{Trust, p.TrustID})
			if e != nil {
				return e
			}
			var trust trustRecord
			if r.Revision == 0 || json.Unmarshal(r.Data, &trust) != nil {
				return ErrCorrupt
			}
			if trust.Revoked {
				return ErrRevoked
			}
			if r.Revision != p.TrustRevision {
				return ErrConflict
			}
		}
		out = SendResult{Data: v.Data, Reused: true}
		return nil
	})
	if err != nil {
		return SendResult{}, err
	}
	return out, nil
}

// OutboundRecord is the protected committed operation, not a network frame.
type OutboundRecord = sentBatch

func (r SendResult) Operation() (OutboundRecord, error) {
	var out OutboundRecord
	e := json.Unmarshal(r.Data, &out)
	return out, e
}
