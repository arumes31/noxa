package durable

import (
	"bytes"
	"encoding/json"
	"errors"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/ratchet"
	"strings"
	"testing"
)

func TestAttemptReceiptAndHistoricalResolution(t *testing.T) {
	s := openTest(t, t.TempDir())
	defer s.Close()
	trusted(t, s, "bob")
	m := admission.Manifest{Version: admission.Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: "alice", Device: "local-device", AccountEpoch: 1, AttemptID: "1", LogicalID: "logical", Purpose: "chat", Scope: "direct", Peer: "bob", Deadline: 1900000000, Rosters: []admission.Roster{{Principal: "alice", Version: 1}, {Principal: "bob", Version: 1}}, Targets: []admission.Target{{Principal: "alice", Device: "local-device", AccountEpoch: 1, Identity: "alice-key", Local: true}, {Principal: "bob", Device: "bob-device", AccountEpoch: 1, Identity: "bob-olm"}}}
	// Namespace equality is enforced by the attempt adapter.
	s.Close()
	var e error
	s, e = Open(t.TempDir(), Namespace{m.Server, "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	trusted(t, s, "bob")
	target := m.Targets[1]
	bind := admission.ExpectedBinding(m, target)
	env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "session", Binding: bind, Message: json.RawMessage(`{"type":0,"body":"cipher"}`)})
	context := contextFor("bob")
	parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "session", Envelope: env}})
	if e != nil {
		t.Fatal(e)
	}
	snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "bob"}, Ref{Logical, "logical"})
	out, e := s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte("candidate"), Envelope: env, Context: context}}, []byte("readable secret"))
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
	snap = readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "bob"}, Ref{Logical, "logical"})
	if _, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte("advance"), Envelope: env, Context: context}}, []byte("changed")); !errors.Is(e, ErrConflict) {
		t.Fatal("accepted intent resealed", e)
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
		wrong.Close()
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
	defer s.Close()
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
		parts, e := admission.Freeze(&m, []admission.Copy{{Principal: "bob", Device: "bob-device", AccountEpoch: 1, SessionID: "pair", Envelope: env}})
		if e != nil {
			return e
		}
		snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "pair"}, Ref{Logical, m.LogicalID})
		_, e = s.CommitAttempt(snap, m, parts, []SendCandidate{{SessionID: "pair", TrustID: "bob", State: []byte("state-" + m.AttemptID), Envelope: env, Context: context}}, []byte("archive"))
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
