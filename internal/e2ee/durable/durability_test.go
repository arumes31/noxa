package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"noxa/internal/e2ee/ratchet"
)

var testNS = Namespace{"authenticated-server", "alice", "local-device"}

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, e := Open(dir, testNS, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 100*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func readTest(t *testing.T, s *Store, refs ...Ref) Snapshot {
	t.Helper()
	v, e := s.Read(refs...)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func trusted(t *testing.T, s *Store, id string) {
	t.Helper()
	snap := readTest(t, s, Ref{Trust, id})
	if e := s.SetTrust(snap, id, []byte("independently verified peer"), false); e != nil {
		t.Fatal(e)
	}
}
func applyTest(t *testing.T, e *ratchet.Engine, r ratchet.Request) ratchet.Response {
	t.Helper()
	v, err := e.Apply(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func contextFor(peer string) SessionContext {
	return SessionContext{peer, peer + "-device", peer + "-olm", "alice:bob", "noxa-olm-v1"}
}
func TestRealCoreAtomicFanoutReceive(t *testing.T) {
	a := openTest(t, t.TempDir())
	defer a.Close()
	b := openTest(t, t.TempDir())
	defer b.Close()
	trusted(t, a, "bob")
	trusted(t, b, "alice")
	core, err := ratchet.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close(context.Background())
	alice := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: a.CoreKey(), Count: 1})
	bob := applyTest(t, core, ratchet.Request{Op: ratchet.NewAccount, Key: b.CoreKey(), Count: 1})
	for _, v := range []struct {
		s *Store
		r ratchet.Response
	}{{a, alice}, {b, bob}} {
		snap := readTest(t, v.s, Ref{Account, "local"})
		id := v.r.Bundle.OneTimeKeys[0].ID
		if _, err = v.s.CommitPublication(snap, "pub1", []byte(v.r.Account), []byte("exact signed publication"), map[string]OTKObligation{id: {PublicationID: "pub1", ExpiresAt: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	binding := &ratchet.Binding{Server: testNS.Server, Conversation: "alice:bob", Sender: "alice", SenderDevice: "a", Recipient: "bob", RecipientDevice: "b", MessageID: "first", Purpose: "message"}
	sent := applyTest(t, core, ratchet.Request{Op: ratchet.Outbound, Key: a.CoreKey(), Account: alice.Account, PeerIdentity: bob.Bundle.Identity, OneTimeKey: bob.Bundle.OneTimeKeys[0].Key, Binding: binding, Plaintext: "readable original"})
	snap := readTest(t, a, Ref{Trust, "bob"}, Ref{Session, "bob"})
	out, err := a.CommitSend(snap, "first", []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte(sent.Session), Envelope: encode(sent.Envelope), Context: contextFor("bob")}}, []byte("readable original"))
	if err != nil || out.Reused {
		t.Fatal(err)
	}
	// Real authenticated incoming candidate consumes account/prekey only in commit.
	receivedCore := applyTest(t, core, ratchet.Request{Op: ratchet.Inbound, Key: b.CoreKey(), Account: bob.Account, PeerIdentity: alice.Bundle.Identity, Binding: binding, Envelope: sent.Envelope})
	keyID := bob.Bundle.OneTimeKeys[0].ID
	receiveSnap := readTest(t, b, Ref{Trust, "alice"}, Ref{Session, "alice"}, Ref{Account, "local"}, Ref{Obligation, keyID})
	receive := ReceiveCandidate{SessionID: "alice", TrustID: "alice", Session: []byte(receivedCore.Session), Account: []byte(receivedCore.Account), Archive: []byte(receivedCore.Plaintext), Envelope: encode(sent.Envelope), ConsumedOTK: keyID, Context: contextFor("alice")}
	archive, err := b.CommitReceive(receiveSnap, "first", receive)
	if err != nil || string(archive) != "readable original" {
		t.Fatal(err)
	}
	receive.Archive = []byte("changed untrusted caller text")
	archive, err = b.CommitReceive(receiveSnap, "first", receive)
	if err != nil || string(archive) != "readable original" {
		t.Fatalf("duplicate archive: %v", err)
	}
	after := readTest(t, b, Ref{Account, "local"}, Ref{Session, "alice"}, Ref{Inbox, "first"}, Ref{Obligation, keyID})
	if after.Records[Ref{Account, "local"}].Revision != 2 || after.Records[Ref{Session, "alice"}].Revision != 1 {
		t.Fatal("duplicate advanced state")
	}
	if _, err = core.Apply(context.Background(), ratchet.Request{Op: ratchet.Inbound, Key: b.CoreKey(), Account: string(after.Records[Ref{Account, "local"}].Data), PeerIdentity: alice.Bundle.Identity, Binding: binding, Envelope: sent.Envelope}); err == nil {
		t.Fatal("consumed OTK reusable")
	}
	bad := *sent.Envelope
	bad.Binding.MessageID = "tampered"
	before := after.Records[Ref{Session, "alice"}].Revision
	if _, err = core.Apply(context.Background(), ratchet.Request{Op: ratchet.Inbound, Key: b.CoreKey(), Account: bob.Account, PeerIdentity: alice.Bundle.Identity, Binding: &bad.Binding, Envelope: &bad}); err == nil {
		t.Fatal("bad auth accepted")
	}
	if readTest(t, b, Ref{Session, "alice"}).Records[Ref{Session, "alice"}].Revision != before {
		t.Fatal("failed auth mutated store")
	}
	// Ten-device fanout is all-or-none even if the last peer is revoked.
	var refs []Ref
	var packets []SendCandidate
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("device-%d", i)
		trusted(t, a, id)
		refs = append(refs, Ref{Trust, id}, Ref{Session, id})
		packets = append(packets, SendCandidate{SessionID: id, TrustID: id, State: []byte(sent.Session), Envelope: encode(sent.Envelope), Context: contextFor(id)})
	}
	fan := readTest(t, a, refs...)
	rev := readTest(t, a, Ref{Trust, "device-9"})
	if err = a.SetTrust(rev, "device-9", []byte("revoked"), true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CommitSend(fan, "fanout", packets, []byte("group archive")); !errors.Is(err, ErrConflict) {
		t.Fatalf("revocation CAS: %v", err)
	}
	current := readTest(t, a, append(refs, Ref{Outbox, "fanout"})...)
	for _, p := range packets {
		if current.Records[Ref{Session, p.SessionID}].Revision != 0 {
			t.Fatal("partial fanout committed")
		}
	}
	// A different fresh roster with all ten trusted devices does commit together.
	packets = packets[:9]
	packets = append(packets, SendCandidate{SessionID: "device-10", TrustID: "device-10", State: []byte(sent.Session), Envelope: encode(sent.Envelope), Context: contextFor("device-10")})
	trusted(t, a, "device-10")
	refs = refs[:18]
	refs = append(refs, Ref{Trust, "device-10"}, Ref{Session, "device-10"})
	fan = readTest(t, a, refs...)
	if _, err = a.CommitSend(fan, "fanout", packets, []byte("group archive")); err != nil {
		t.Fatal(err)
	}
	ack := readTest(t, a, Ref{Outbox, "fanout"})
	if err = a.Ack(ack, "fanout"); err != nil {
		t.Fatal(err)
	}
	if readTest(t, a, Ref{Session, "device-0"}).Records[Ref{Session, "device-0"}].Revision != 1 {
		t.Fatal("ack rewound state")
	}
}
func TestCorruptionAndBackendCapacityFailure(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	trusted(t, s, "bob")
	snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "bob"})
	// Actual bbolt allocation failure after candidate writes, before DB commit.
	s.db.MaxSize = 32768
	_, err := s.CommitSend(snap, "too-large", []SendCandidate{{SessionID: "bob", TrustID: "bob", State: bytes.Repeat([]byte{11}, 1<<20), Envelope: []byte("exact cipher"), Context: contextFor("bob")}}, []byte("archive"))
	if err == nil {
		t.Fatal("backend allocation unexpectedly succeeded")
	}
	s.db.MaxSize = 0
	got := readTest(t, s, Ref{Session, "bob"}, Ref{Outbox, "too-large"})
	for _, v := range got.Records {
		if v.Revision != 0 {
			t.Fatal("failed commit published partial state")
		}
	}
	if err = s.db.Update(func(tx *bolt.Tx) error {
		v := append([]byte(nil), tx.Bucket(bucket).Get(keyOf(Ref{Trust, "bob"}))...)
		v[len(v)-1] ^= 1
		return tx.Bucket(bucket).Put(keyOf(Ref{Trust, "bob"}), v)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(Ref{Trust, "bob"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corruption accepted %v", err)
	}
	file := s.db.Path()
	s.Close()
	raw, _ := os.ReadFile(file)
	if bytes.Contains(raw, []byte("independently verified peer")) {
		t.Fatal("plaintext on disk")
	}
	// Key corruption is fatal and is never overwritten with a newly generated key.
	db, err := bolt.Open(file, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error { return tx.Bucket(keyBucket).Put([]byte("master"), []byte("broken")) })
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, testNS, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("key reset %v", err)
	}
}
func TestPrivateOTKObligationsNeverExpire(t *testing.T) {
	s := openTest(t, t.TempDir())
	defer s.Close()
	otks := map[string]OTKObligation{}
	for i := 0; i < 100; i++ {
		otks[fmt.Sprint(i)] = OTKObligation{PublicationID: "pub", ExpiresAt: 1}
	}
	snap := readTest(t, s, Ref{Account, "local"})
	if _, e := s.CommitPublication(snap, "pub", []byte("account with private keys"), []byte("signed pub"), otks); e != nil {
		t.Fatal(e)
	}
	snap = readTest(t, s, Ref{Account, "local"})
	if _, e := s.CommitPublication(snap, "next", []byte("next account"), []byte("signed next"), map[string]OTKObligation{"new": {PublicationID: "next", ExpiresAt: 1}}); !errors.Is(e, ErrExhausted) {
		t.Fatalf("expired obligation discarded: %v", e)
	}
	if len(readTest(t, s, Ref{Obligation, "0"}).Records[Ref{Obligation, "0"}].Data) == 0 {
		t.Fatal("lost old obligation")
	}
}

// This helper is run as an actual OS subprocess, never as a goroutine.
func TestStoreSubprocess(t *testing.T) {
	mode := os.Getenv("NOXA_STORE_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("NOXA_STORE_DIR")
	protection := IdentityProtection(bytes.Repeat([]byte{7}, 32))
	if mode == "initialize" {
		protection.Seal = func([]byte) ([]byte, error) {
			if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("before first commit"), 0600); err != nil {
				t.Fatal(err)
			}
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	s, e := Open(dir, testNS, protection, 100*time.Millisecond)
	if mode == "lock" {
		if !errors.Is(e, ErrInUse) {
			t.Fatalf("expected exclusive lock: %v", e)
		}
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	snap := readTest(t, s, Ref{Trust, "bob"}, Ref{Session, "bob"})
	if mode == "before" {
		tx, e := s.db.Begin(true)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = s.put(tx, Ref{Session, "bob"}, Record{1, []byte("uncommitted state")}); e != nil {
			t.Fatal(e)
		}
		if e = s.put(tx, Ref{Outbox, "crash"}, Record{1, []byte("uncommitted ciphertext")}); e != nil {
			t.Fatal(e)
		}
	} else {
		_, e = s.CommitSend(snap, "crash", []SendCandidate{{SessionID: "bob", TrustID: "bob", State: []byte("committed state"), Envelope: []byte("committed ciphertext"), Context: contextFor("bob")}}, []byte("original archive"))
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); e != nil {
		t.Fatal(e)
	}
	for {
		time.Sleep(time.Hour)
	}
}
func TestActualProcessLockAndKill(t *testing.T) {
	for _, mode := range []string{"initialize", "before", "after"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			var s *Store
			var cmd *exec.Cmd
			if mode != "initialize" {
				s = openTest(t, dir)
				trusted(t, s, "bob")
				cmd = exec.Command(os.Args[0], "-test.run=^TestStoreSubprocess$")
				cmd.Env = append(os.Environ(), "NOXA_STORE_HELPER=lock", "NOXA_STORE_DIR="+dir)
				if output, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("lock child: %s %v", output, e)
				}
				s.Close()
			}
			cmd = exec.Command(os.Args[0], "-test.run=^TestStoreSubprocess$")
			cmd.Env = append(os.Environ(), "NOXA_STORE_HELPER="+mode, "NOXA_STORE_DIR="+dir)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(dir, "ready")); e == nil {
					break
				}
				if time.Now().After(deadline) {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("helper not ready: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e := cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			_ = cmd.Wait()
			s = openTest(t, dir)
			defer s.Close()
			got := readTest(t, s, Ref{Session, "bob"}, Ref{Outbox, "crash"})
			for _, v := range got.Records {
				if mode != "after" && v.Revision != 0 {
					t.Fatal("uncommitted bytes visible")
				}
				if mode == "after" && v.Revision != 1 {
					t.Fatal("committed bytes lost")
				}
			}
			if mode == "after" {
				var b sentBatch
				if e := json.Unmarshal(got.Records[Ref{Outbox, "crash"}].Data, &b); e != nil || string(b.Packets[0].Envelope) != "committed ciphertext" {
					t.Fatal("exact retry bytes lost")
				}
			}
		})
	}
}
func TestNamespaceReplacementRecoveryAndBounds(t *testing.T) {
	s := openTest(t, t.TempDir())
	defer s.Close()
	trusted(t, s, "peer")
	other, err := Open(t.TempDir(), Namespace{"other-server", "alice", "local-device"}, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	snap := readTest(t, s, Ref{Trust, "peer"}, Ref{Session, "old"})
	if err = other.SetTrust(snap, "peer", []byte("must not cross namespace"), false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross namespace accepted %v", err)
	}
	packet := SendCandidate{SessionID: "old", TrustID: "peer", State: []byte("opaque protected snapshot"), Envelope: []byte("exact cipher"), Context: contextFor("peer")}
	committed, err := s.CommitSend(snap, "old-send", []SendCandidate{packet}, []byte("original readable archive"))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.RetrySend(snap.Fence, "old-send")
	if err != nil || !bytes.Equal(retry.Data, committed.Data) || !retry.Reused {
		t.Fatalf("recovery retry %v", err)
	}
	refs, err := s.PendingRefs(Outbox, "", 1)
	if err != nil || len(refs) != 1 || refs[0].ID != "old-send" {
		t.Fatalf("pending refs %v %v", refs, err)
	}
	snap = readTest(t, s, Ref{Trust, "peer"}, Ref{Session, "old"}, Ref{Session, "new"})
	packet.SessionID = "new"
	packet.Replaces = "old"
	if _, err = s.CommitSend(snap, "new-send", []SendCandidate{packet}, []byte("new readable archive")); err != nil {
		t.Fatal(err)
	}
	snap = readTest(t, s, Ref{Trust, "peer"}, Ref{Session, "old"})
	packet.SessionID = "old"
	packet.Replaces = ""
	if _, err = s.CommitSend(snap, "retired-send", []SendCandidate{packet}, []byte("must fail")); !errors.Is(err, ErrConflict) {
		t.Fatalf("retired send %v", err)
	}
	// Retirement prevents new sends, but delayed receives can still update it.
	if _, err = s.CommitReceive(snap, "late-receive", ReceiveCandidate{SessionID: "old", TrustID: "peer", Session: packet.State, Envelope: packet.Envelope, Archive: []byte("late readable"), Context: packet.Context}); err != nil {
		t.Fatal(err)
	}
	refs = nil
	packets := make([]SendCandidate, MaxFanout)
	for i := range packets {
		id := fmt.Sprintf("group-%d", i)
		refs = append(refs, Ref{Session, id})
		packets[i] = SendCandidate{SessionID: id, TrustID: "peer", State: []byte("protected core snapshot"), Envelope: bytes.Repeat([]byte{65}, 90000), Context: contextFor("peer")}
	}
	refs = append(refs, Ref{Trust, "peer"})
	snap = readTest(t, s, refs...)
	if _, err = s.CommitSend(snap, "group160", packets, bytes.Repeat([]byte{66}, 65536)); err != nil {
		t.Fatalf("default 160-device bound %v", err)
	}
	if _, err = s.CommitSend(snap, "group161", append(packets, packet), []byte("archive")); !errors.Is(err, ErrExhausted) {
		t.Fatalf("fanout overflow %v", err)
	}
	// Two explicit account IDs remain under the same logout transaction/fence.
	for _, id := range []string{"epoch-1", "epoch-2"} {
		snap = readTest(t, s, Ref{Account, id})
		if _, err = s.CommitPublicationForAccount(snap, id, "pub-"+id, []byte("protected account"), []byte("signed pub"), map[string]OTKObligation{"otk-" + id: {PublicationID: "pub-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	oldFence := snap.Fence
	if err = s.Logout(oldFence); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(Ref{Account, "epoch-1"}, Ref{Account, "epoch-2"}); !errors.Is(err, ErrFenced) {
		t.Fatal("logout left account epoch active")
	}
}

// First initialization is recoverable only when no committed bucket exists.
func TestInitializationDoesNotResetPartialOrUnknownState(t *testing.T) {
	for _, damage := range []string{"unexpected bucket", "extra bucket", "missing master", "missing control", "missing payload", "missing key bucket"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			s := openTest(t, dir)
			file := s.db.Path()
			s.Close()
			db, err := bolt.Open(file, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				switch damage {
				case "extra bucket":
					_, e := tx.CreateBucket([]byte("unexpected"))
					return e
				case "unexpected bucket":
					if e := tx.DeleteBucket(keyBucket); e != nil {
						return e
					}
					if e := tx.DeleteBucket(bucket); e != nil {
						return e
					}
					_, e := tx.CreateBucket([]byte("unexpected"))
					return e
				case "missing master":
					return tx.Bucket(keyBucket).Delete([]byte("master"))
				case "missing control":
					return tx.Bucket(bucket).Delete(keyOf(controlRef))
				case "missing payload":
					return tx.DeleteBucket(bucket)
				case "missing key bucket":
					return tx.DeleteBucket(keyBucket)
				}
				return nil
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, testNS, IdentityProtection(bytes.Repeat([]byte{7}, 32)), 0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s: %v", damage, err)
			}
		})
	}
}
