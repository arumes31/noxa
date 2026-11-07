package durable

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/e2ee/ratchet"
)

// Disappearing messages: a redacted archive replaces the readable text in both
// directions while delivery/dedupe metadata stays intact.
func TestRedactArchiveKeepsDedupeAndRefusesUnsentOutbox(t *testing.T) {
	a := openTest(t, t.TempDir())
	defer func() { _ = a.Close() }()
	b := openTest(t, t.TempDir())
	defer func() { _ = b.Close() }()
	trusted(t, a, "bob")
	trusted(t, b, "alice")
	core, err := ratchet.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close(context.Background()) }()
	alice := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: a.CoreKey(), Count: 1})
	bob := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: b.CoreKey(), Count: 1})
	for _, v := range []struct {
		s *Store
		r ratchet.Response
	}{{a, alice}, {b, bob}} {
		snap := readTest(t, v.s, Ref{Account, "local"})
		id := v.r.Bundle.OneTimeKeys[0].ID
		if _, err = v.s.CommitPublication(snap, "pub1", []byte(v.r.Account), []byte("signed publication"), map[string]OTKObligation{id: {PublicationID: "pub1", ExpiresAt: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	binding := &ratchet.Binding{Server: testNS.Server, Conversation: "alice:bob", Sender: "alice", SenderDevice: "a", Recipient: "bob", RecipientDevice: "b", MessageID: "m1", Purpose: "message"}
	sent := applyTest(t, core, ratchet.Request{Op: ratchet.Outbound, Key: a.CoreKey(), Account: alice.Account, PeerIdentity: bob.Bundle.Identity, OneTimeKey: bob.Bundle.OneTimeKeys[0].Key, Binding: binding, Plaintext: "secret text"})
	snap := readTest(t, a, Ref{Trust, "bob"}, Ref{Session, "bob"})
	if _, err = a.CommitSend(snap, "m1", []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte(sent.Session), Envelope: encode(sent.Envelope), Context: contextFor("bob")}}, []byte("secret text")); err != nil {
		t.Fatal(err)
	}

	// Outbox: a batch not yet sent can still be needed, so redaction waits.
	outRef := Ref{Outbox, "m1"}
	if err = a.RedactArchive(readTest(t, a, outRef), outRef, []byte("expired")); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsent outbox redacted: %v", err)
	}
	if err = a.Ack(readTest(t, a, outRef), "m1"); err != nil {
		t.Fatal(err)
	}
	if err = a.RedactArchive(readTest(t, a, outRef), outRef, []byte("expired")); err != nil {
		t.Fatal(err)
	}
	op, err := SendResult{Data: readTest(t, a, outRef).Records[outRef].Data}.Operation()
	if err != nil || string(op.Archive) != "expired" || op.Status != "sent" || len(op.Packets) != 1 {
		t.Fatalf("outbox after redaction: %+v %v", op, err)
	}

	// Inbox: the original is replaced; a redelivery still matches the digest
	// and returns the redacted archive, never the original text again.
	received := applyTest(t, core, ratchet.Request{Op: ratchet.Inbound, Key: b.CoreKey(), Account: bob.Account, PeerIdentity: alice.Bundle.Identity, Binding: binding, Envelope: sent.Envelope})
	keyID := bob.Bundle.OneTimeKeys[0].ID
	receiveSnap := readTest(t, b, Ref{Trust, "alice"}, Ref{Session, "alice"}, Ref{Account, "local"}, Ref{Obligation, keyID})
	candidate := ReceiveCandidate{SessionID: "alice", TrustID: "alice", Session: []byte(received.Session), Account: []byte(received.Account), Archive: []byte(received.Plaintext), Envelope: encode(sent.Envelope), ConsumedOTK: keyID, Context: contextFor("alice")}
	if _, err = b.CommitReceive(receiveSnap, "m1", candidate); err != nil {
		t.Fatal(err)
	}
	inRef := Ref{Inbox, "m1"}
	stale := readTest(t, b, inRef)
	if err = b.RedactArchive(stale, inRef, []byte("expired")); err != nil {
		t.Fatal(err)
	}
	// The same snapshot is stale now: optimistic concurrency refuses it.
	if err = b.RedactArchive(stale, inRef, []byte("again")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale snapshot accepted: %v", err)
	}
	archive, err := b.CommitReceive(receiveSnap, "m1", candidate)
	if err != nil || string(archive) != "expired" {
		t.Fatalf("redelivery returned %q, %v", archive, err)
	}
	after := readTest(t, b, Ref{Session, "alice"}, Ref{Account, "local"})
	if after.Records[Ref{Session, "alice"}].Revision != 1 || after.Records[Ref{Account, "local"}].Revision != 2 {
		t.Fatal("redaction touched session or account state")
	}

	// Only existing inbox/outbox records in the snapshot can be redacted.
	for _, bad := range []struct {
		ref  Ref
		data []byte
	}{{Ref{Session, "alice"}, []byte("x")}, {Ref{Inbox, "missing"}, []byte("x")}, {inRef, nil}} {
		if err = b.RedactArchive(readTest(t, b, bad.ref), bad.ref, bad.data); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v redacted: %v", bad.ref, err)
		}
	}
}
