package durable

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/ratchet"

	bolt "go.etcd.io/bbolt"
	"strings"
	"testing"
)

func TestAttemptReceiptAndHistoricalResolution(t *testing.T) {
	s := openTest(t, t.TempDir())
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	m := admission.Manifest{Version: admission.Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: "alice", Device: "local-device", AccountEpoch: 1, AttemptID: "1", LogicalID: "logical", Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: 1900000000, Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}}, Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
	// Namespace equality is enforced by the attempt adapter.
	_ = s.Close()
	var e error
	s, e = Open(t.TempDir(), Namespace{m.Server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	target := m.Targets[1]
	bind := admission.ExpectedBinding(m, target)
	env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "session", Binding: bind, Message: json.RawMessage(`{"type":0,"body":"cipher"}`)})
	context := contextFor("bob")
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "session", ClaimID: "c1", Envelope: env}})
	if e != nil {
		t.Fatal(e)
	}
	first := &SessionClaim{ClaimID: "c1", ClaimUntil: 1900000000}
	snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:session"}, Ref{Logical, "logical"})
	out, e := s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "olm:session", TrustID: "bob", State: []byte("candidate"), Envelope: env, Context: context, Claim: first}}, []byte("readable secret"))
	if e != nil {
		t.Fatal(e)
	}
	op, e := out.Operation()
	if e != nil || op.Attempt == nil {
		t.Fatal(e)
	}
	metadata, e := s.AttemptStatusRequest(snap.Fence, AttemptID(m))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(encode(metadata), []byte("readable secret")) || bytes.Contains(encode(metadata), []byte("cipher")) {
		t.Fatal("metadata exposes local archive/ciphertext")
	}
	// Trust changes prohibit packet retry but preserve metadata-only resolution.
	trusted(t, s, "bob")
	if _, e = s.RetrySend(snap.Fence, AttemptID(m)); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	mbytes, _ := m.Bytes()
	r := admission.Receipt{State: "accepted", AttemptID: m.AttemptID, Digest: admission.Digest(mbytes), Server: m.Server, AccountEpoch: 1, LogicalID: m.LogicalID, MessageID: "message"}
	snap = readTest(t, s, Ref{Outbox, AttemptID(m)}, Ref{Logical, "logical"})
	if e = s.ApplyAttemptResult(snap, AttemptID(m), r); e != nil {
		t.Fatal(e)
	}
	ack, e := s.AttemptRetirementRequest(snap.Fence, AttemptID(m))
	if e != nil || ack.Digest != r.Digest {
		t.Fatal(ack, e)
	}
	m.AttemptID = "2"
	snap = readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:session"}, Ref{Logical, "logical"})
	if _, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "olm:session", TrustID: "bob", State: []byte("advance"), Envelope: env, Context: context, Claim: first}}, []byte("changed")); !errors.Is(e, ErrConflict) {
		t.Fatal("accepted intent resealed", e)
	}
	// Durable retire_receipt progress: only an attempt with a committed receipt
	// of the current epoch, idempotent, and visible through AccountAuthority.
	if e = s.NoteRetired(readTest(t, s), AttemptID(m)); !errors.Is(e, ErrInvalid) {
		t.Fatal("retired without the outbox in the snapshot", e)
	}
	if e = s.NoteRetired(readTest(t, s, Ref{Outbox, "1:7"}), "1:7"); !errors.Is(e, ErrInvalid) {
		t.Fatal("retired an unknown attempt", e)
	}
	for i := 0; i < 2; i++ {
		if e = s.NoteRetired(readTest(t, s, Ref{Outbox, "1:1"}), "1:1"); e != nil {
			t.Fatal(e)
		}
	}
	a, e := s.AccountAuthority(snap.Fence)
	if e != nil || a.Retired.Through != 1 || len(a.Retired.Above) != 0 || !a.Retired.Has(1) || a.Retired.Has(2) {
		t.Fatal("retirement watermark", a, e)
	}
}
func TestSharedStoreReferences(t *testing.T) {
	dir := t.TempDir()
	p := IdentityProtection(bytes.Repeat([]byte{7}, 32))
	a, e := OpenShared(dir, testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	b, e := OpenShared(dir, testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	if wrong, e := OpenShared(dir, testNS, IdentityProtection(bytes.Repeat([]byte{9}, 32))); e == nil {
		_ = wrong.Close()
		t.Fatal("shared handle bypassed identity protection")
	}
	if a.Store != b.Store {
		t.Fatal("different process-local ratchets")
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Read(); e != nil {
		t.Fatal(e)
	}
	if e = b.Close(); e != nil {
		t.Fatal(e)
	}
}

// One Olm session per device pair serves direct AND group attempts; legacy
// per-conversation contexts are invalid and never mixed into it.
func TestDevicePairSessionAcrossConversations(t *testing.T) {
	server := "tls-sha256:" + strings.Repeat("a", 64)
	s, e := Open(t.TempDir(), Namespace{server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	base := admission.Manifest{Version: admission.Version, Server: server, Sender: "alice", Device: "local-device", AccountEpoch: 1, Purpose: "chat", Deadline: 1900000000,
		Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}},
		Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
	direct, group := base, base
	direct.AttemptID, direct.LogicalID, direct.Scope, direct.Peer = "1", "dm", "direct", "bob"
	group.AttemptID, group.LogicalID, group.Scope, group.Conversation, group.Revision, group.Epoch = "2", "group-msg", "group", "g1", 1, 1
	commit := func(m admission.Manifest, context SessionContext) error {
		target := m.Targets[1]
		env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "pair", Binding: admission.ExpectedBinding(m, target), Message: json.RawMessage(`{"type":1,"body":"cipher"}`)})
		// First contact: the device-pair session is created (and stays pending)
		// with one reserved claim, reused by the group attempt.
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "pair", ClaimID: "c-pair", Envelope: env}})
		if e != nil {
			return e
		}
		snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:pair"}, Ref{Logical, m.LogicalID})
		_, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "olm:pair", TrustID: "bob", State: []byte("state-" + m.AttemptID), Envelope: env, Context: context, Claim: &SessionClaim{ClaimID: "c-pair", ClaimUntil: 1900000000}}}, []byte("archive"))
		return e
	}
	legacy := contextFor("bob")
	legacy.Scope = admission.ExpectedBinding(direct, direct.Targets[1]).Conversation
	if e = commit(direct, legacy); !errors.Is(e, ErrInvalid) {
		t.Fatal("per-conversation session context accepted", e)
	}
	if e = commit(direct, contextFor("bob")); e != nil {
		t.Fatal(e)
	}
	if e = commit(group, contextFor("bob")); e != nil {
		t.Fatal("device-pair session not reusable by a group conversation", e)
	}
}

// F1: a first-contact session stays Pending (carrying its reserved claim)
// until an attempt using it is accepted; aborted/rejected attempts keep the
// claim for an exact resend, and acceptance clears it.
func TestPendingFirstContactSessionClaim(t *testing.T) {
	server := "tls-sha256:" + strings.Repeat("a", 64)
	s, e := Open(t.TempDir(), Namespace{server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	claim := &SessionClaim{ClaimID: "claim-1", ClaimUntil: 1900000000}
	seq := 0
	commit := func(logical string, c *SessionClaim) (admission.Manifest, error) {
		seq++
		m := admission.Manifest{Version: admission.Version, Server: server, Sender: "alice", Device: "local-device", AccountEpoch: 1, AttemptID: fmt.Sprint(seq), LogicalID: logical, Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: 1900000000,
			Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}},
			Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
		env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "pair", Binding: admission.ExpectedBinding(m, m.Targets[1]), Message: json.RawMessage(`{"type":0,"body":"cipher"}`)})
		copyClaim := ""
		if c != nil {
			copyClaim = c.ClaimID
		}
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "pair", ClaimID: copyClaim, Envelope: env}})
		if e != nil {
			return m, e
		}
		snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:pair"}, Ref{Logical, logical})
		_, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "olm:pair", TrustID: "bob", State: []byte("state-" + m.AttemptID), Envelope: env, Context: contextFor("bob"), Claim: c}}, []byte("archive"))
		if e != nil {
			seq--
		}
		return m, e
	}
	apply := func(m admission.Manifest, state string) {
		mb, _ := m.Bytes()
		r := admission.Receipt{State: state, AttemptID: m.AttemptID, Digest: admission.Digest(mb), Server: server, AccountEpoch: 1, LogicalID: m.LogicalID}
		if state == "accepted" {
			r.MessageID = "message"
		}
		snap := readTest(t, s, Ref{Outbox, AttemptID(m)}, Ref{Logical, m.LogicalID})
		if e := s.ApplyAttemptResult(snap, AttemptID(m), r); e != nil {
			t.Fatal(e)
		}
	}
	pending := func() *SessionClaim {
		var st SessionState
		_ = json.Unmarshal(readTest(t, s, Ref{Session, "olm:pair"}).Records[Ref{Session, "olm:pair"}].Data, &st)
		return st.Pending
	}
	m1, e := commit("first", claim)
	if e != nil || pending() == nil || *pending() != *claim {
		t.Fatal("first contact not pending with its claim", e, pending())
	}
	apply(m1, "aborted")
	if pending() == nil {
		t.Fatal("aborted first contact lost its claim")
	}
	if _, e = commit("first-resend", nil); !errors.Is(e, ErrConflict) {
		t.Fatal("pending session used without its claim", e)
	}
	if _, e = commit("first-resend", &SessionClaim{ClaimID: "other", ClaimUntil: 1900000000}); !errors.Is(e, ErrConflict) {
		t.Fatal("pending session accepted a different claim", e)
	}
	m2, e := commit("first-resend", claim)
	if e != nil {
		t.Fatal(e)
	}
	apply(m2, "accepted")
	if pending() != nil {
		t.Fatal("accepted attempt did not establish the session")
	}
	if _, e = commit("later", claim); !errors.Is(e, ErrConflict) {
		t.Fatal("established session re-acquired a claim", e)
	}
}

// N4/N5: wire copies must name the packet's session and claim; a new session
// in an attempt needs a claim; and a pending session whose claim can no
// longer cover a resend is replaced (old retired) in the same commit.
func TestAttemptBindingAndPendingReplacement(t *testing.T) {
	server := "tls-sha256:" + strings.Repeat("a", 64)
	s, e := Open(t.TempDir(), Namespace{server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	seq := 0
	commit := func(logical, sessionRef, coreID, copyClaim string, c *SessionClaim, replaces string) (admission.Manifest, error) {
		seq++
		m := admission.Manifest{Version: admission.Version, Server: server, Sender: "alice", Device: "local-device", AccountEpoch: 1, AttemptID: fmt.Sprint(seq), LogicalID: logical, Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: 1900000000,
			Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}},
			Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
		env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: coreID, Binding: admission.ExpectedBinding(m, m.Targets[1]), Message: json.RawMessage(`{"type":0,"body":"cipher"}`)})
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: coreID, ClaimID: copyClaim, Envelope: env}})
		if e != nil {
			return m, e
		}
		refs := []Ref{{Trust, "bob"}, {Session, sessionRef}, {Logical, logical}}
		if replaces != "" {
			refs = append(refs, Ref{Session, replaces})
		}
		_, e = s.CommitAttempt(readTest(t, s, refs...), m, parts, []SendCandidate{{SessionID: sessionRef, TrustID: "bob", State: []byte("state"), Envelope: env, Context: contextFor("bob"), Claim: c, Replaces: replaces}}, []byte("archive"))
		if e != nil {
			seq--
		}
		return m, e
	}
	c1 := &SessionClaim{ClaimID: "c1", ClaimUntil: 1900000000}
	if _, e = commit("x", "olm:other", "s1", "c1", c1, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal("packet session not bound to the copy's session", e)
	}
	if _, e = commit("x", "olm:s1", "s1", "c2", c1, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal("copy claim differs from packet claim", e)
	}
	if _, e = commit("x", "olm:s1", "s1", "", nil, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal("new attempt session without a claim", e)
	}
	m1, e := commit("first", "olm:s1", "s1", "c1", c1, "")
	if e != nil {
		t.Fatal(e)
	}
	mb, _ := m1.Bytes()
	snap := readTest(t, s, Ref{Outbox, AttemptID(m1)}, Ref{Logical, "first"})
	if e = s.ApplyAttemptResult(snap, AttemptID(m1), admission.Receipt{State: "aborted", AttemptID: m1.AttemptID, Digest: admission.Digest(mb), Server: server, AccountEpoch: 1, LogicalID: "first"}); e != nil {
		t.Fatal(e)
	}
	// The claim can no longer cover a resend: replace the pending session.
	c2 := &SessionClaim{ClaimID: "c2", ClaimUntil: 1900003600}
	if _, e = commit("first-again", "olm:s2", "s2", "c2", c2, "olm:s1"); e != nil {
		t.Fatal("pending session replacement", e)
	}
	read := func(id string) SessionState {
		var st SessionState
		_ = json.Unmarshal(readTest(t, s, Ref{Session, id}).Records[Ref{Session, id}].Data, &st)
		return st
	}
	if old, next := read("olm:s1"), read("olm:s2"); !old.Retired || next.Retired || next.Pending == nil || *next.Pending != *c2 {
		t.Fatal("replacement did not retire the old session and pend the new claim", old, next)
	}
}

// Finished key controls leave nothing behind: a retired control attempt is
// deleted with its logical record, and a namespace already full of the ones
// earlier versions kept accepts sends again after ForgetRetiredControls.
func TestRetiredControlsAreForgotten(t *testing.T) {
	server := "tls-sha256:" + strings.Repeat("a", 64)
	s, e := Open(t.TempDir(), Namespace{server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	trusted(t, s, "bob")
	big := strings.Repeat("c", 1900<<10) // the archive stands in for thousands of real records
	// send commits control attempt n with an accepted receipt and reports the
	// commit error; retire also notes it retired (which deletes it).
	send := func(n int, archive string, retire bool) error {
		logical := fmt.Sprintf("ctl-%d", n)
		m := admission.Manifest{Version: admission.Version, Server: server, Sender: "alice", Device: "local-device", AccountEpoch: 1, AttemptID: fmt.Sprint(n), LogicalID: logical, Purpose: "control", Scope: "direct", Peer: "bob", Deadline: 1900000000, Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}}, Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
		env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "session", Binding: admission.ExpectedBinding(m, m.Targets[1]), Message: json.RawMessage(fmt.Sprintf(`{"type":%d,"body":"cipher"}`, min(n-1, 1)))})
		// Only the first send opens the session (first contact, with a claim).
		claimID, claim := "", (*SessionClaim)(nil)
		if n == 1 {
			claimID, claim = "c1", &SessionClaim{ClaimID: "c1", ClaimUntil: 1900000000}
		}
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "session", ClaimID: claimID, Envelope: env}})
		if e != nil {
			t.Fatal(e)
		}
		id := AttemptID(m)
		snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:session"}, Ref{Logical, logical})
		if _, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "olm:session", TrustID: "bob", State: []byte("state"), Envelope: env, Context: contextFor("bob"), Claim: claim}}, []byte(archive)); e != nil {
			return e
		}
		mbytes, _ := m.Bytes()
		r := admission.Receipt{State: "accepted", AttemptID: m.AttemptID, Digest: admission.Digest(mbytes), Server: server, AccountEpoch: 1, LogicalID: logical, MessageID: "message-" + logical}
		if e = s.ApplyAttemptResult(readTest(t, s, Ref{Outbox, id}, Ref{Logical, logical}), id, r); e != nil {
			t.Fatal(e)
		}
		if retire {
			if e = s.NoteRetired(readTest(t, s, Ref{Outbox, id}), id); e != nil {
				t.Fatal(e)
			}
		}
		return nil
	}
	usage := func() (int, int) {
		count, size, e := s.Usage()
		if e != nil {
			t.Fatal(e)
		}
		return count, size
	}
	if e = send(1, "x", true); e != nil {
		t.Fatal(e)
	}
	count, size := usage()
	if e = send(2, "x", true); e != nil {
		t.Fatal(e)
	}
	if c, b := usage(); c != count || b != size {
		t.Fatalf("a retired control left records behind: %d records %d bytes, was %d and %d", c, b, count, size)
	}
	if v := readTest(t, s, Ref{Outbox, "1:2"}, Ref{Logical, "ctl-2"}); v.Records[Ref{Outbox, "1:2"}].Revision != 0 || v.Records[Ref{Logical, "ctl-2"}].Revision != 0 {
		t.Fatal("retired control still stored")
	}
	// Fill the namespace the way earlier versions did: retired, but kept.
	n := 3
	for ; ; n++ {
		if e = send(n, big, false); errors.Is(e, ErrExhausted) {
			break
		} else if e != nil {
			t.Fatal(e)
		}
	}
	if n < 10 {
		t.Fatalf("namespace full after %d sends", n)
	}
	if e = s.db.Update(func(tx *bolt.Tx) error {
		c, e := s.control(tx)
		if e != nil {
			return e
		}
		c.Retired = &RetiredMark{Epoch: 1, Through: int64(n - 2), Above: []int64{}}
		return s.putControl(tx, c)
	}); e != nil {
		t.Fatal(e)
	}
	freed := 0
	for {
		k, e := s.ForgetRetiredControls(readTest(t, s).Fence, 16)
		if e != nil {
			t.Fatal(e)
		}
		if freed += k; k == 0 {
			break
		}
	}
	// Attempt n-1 is not retired yet and stays; everything before it goes.
	if c, _ := usage(); freed != n-4 || c != count+2 {
		t.Fatalf("freed %d of %d kept attempts, %d records left (baseline %d)", freed, n-3, c, count)
	}
	if e = send(n, big, true); e != nil {
		t.Fatal("still full after the sweep:", e)
	}
	// A received control's record goes once the caller has its acknowledgment.
	snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "olm:session"})
	if _, e = s.CommitReceive(snap, "delivery", ReceiveCandidate{SessionID: "olm:session", TrustID: "bob", Session: []byte("state"), Envelope: []byte("envelope"), Archive: []byte("disposition"), Context: contextFor("bob")}); e != nil {
		t.Fatal(e)
	}
	before, _ := usage()
	if e = s.ForgetReceived(readTest(t, s), "delivery"); e != nil {
		t.Fatal(e)
	}
	if c, _ := usage(); c != before-1 {
		t.Fatalf("received control kept: %d records, was %d", c, before)
	}
}
