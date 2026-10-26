package durable

import (
	"bytes"
	"errors"
	"testing"
)

func TestCommitAtomicRetryAndFence(t *testing.T) {
	s, err := Open(t.TempDir(), Namespace{"server", "alice", "device"}, IdentityProtection(bytes.Repeat([]byte{9}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, _ := s.Read(Ref{Trust, "bob"}, Ref{Session, "bob"})
	if err := s.SetTrust(snap, "bob", []byte("verified identity"), false); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Read(Ref{Trust, "bob"}, Ref{Session, "bob"})
	batch := []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte("next protected snapshot"), Envelope: []byte("ciphertext"), Context: SessionContext{"local", 1, 1, "bob", "device", "olm-key", "alice:bob", "noxa-olm-v1"}}}
	out, err := s.CommitSend(snap, "message", batch, []byte("original readable text"))
	if err != nil {
		t.Fatal(err)
	}
	batch[0].Envelope = []byte("different plaintext candidate")
	retry, err := s.CommitSend(snap, "message", batch, []byte("changed caller draft"))
	if err != nil || !bytes.Equal(out.Data, retry.Data) || !retry.Reused {
		t.Fatalf("immutable retry %v", err)
	}
	if _, err = s.CommitSend(snap, "other", batch, []byte("original readable text")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS %v", err)
	}
	if err = s.Logout(snap.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitSend(snap, "message", batch, []byte("original readable text")); !errors.Is(err, ErrFenced) {
		t.Fatalf("logout retry %v", err)
	}
}

func TestInterruptedFirstInitializationRetries(t *testing.T) {
	dir := t.TempDir()
	ns := Namespace{"server", "alice", "device"}
	protection := IdentityProtection(bytes.Repeat([]byte{9}, 32))
	failed := protection
	failure := errors.New("vault temporarily unavailable")
	failed.Seal = func([]byte) ([]byte, error) { return nil, failure }
	if _, err := Open(dir, ns, failed, 0); !errors.Is(err, failure) {
		t.Fatalf("first initialization: %v", err)
	}
	s, err := Open(dir, ns, protection, 0)
	if err != nil {
		t.Fatalf("retry after rolled-back initialization: %v", err)
	}
	defer s.Close()
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
}
