package ratchet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
)

func TestHandshakeAndAuthentication(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	key := make([]byte, 32)
	key[0] = 42
	call := func(r Request) Response {
		t.Helper()
		r.Key = key
		out, err := e.Apply(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := call(Request{Op: NewAccount, Count: 1})
	b := call(Request{Op: NewAccount, Count: 1})
	binding := Binding{Server: "server-fingerprint", Conversation: "alice:bob", Sender: "alice", SenderDevice: "desktop", Recipient: "bob", RecipientDevice: "phone", MessageID: "message-1", Purpose: "message"}
	send := call(Request{Op: Outbound, Account: a.Account, PeerIdentity: b.Bundle.Identity, OneTimeKey: b.Bundle.OneTimeKeys[0].Key, Binding: &binding, Plaintext: "hello"})
	receive := call(Request{Op: Inbound, Account: b.Account, PeerIdentity: a.Bundle.Identity, Binding: &binding, Envelope: send.Envelope})
	if receive.Plaintext != "hello" || receive.SessionID != send.SessionID {
		t.Fatal("handshake mismatch")
	}
	changed := binding
	changed.Conversation = "other conversation"
	_, err = e.Apply(ctx, Request{Op: Inbound, Key: key, Account: b.Account, PeerIdentity: a.Bundle.Identity, Binding: &changed, Envelope: send.Envelope})
	if err == nil {
		t.Fatal("accepted a cross-conversation message")
	}
	_, err = e.Apply(ctx, Request{Op: Inbound, Key: key, Account: receive.Account, PeerIdentity: a.Bundle.Identity, Binding: &binding, Envelope: send.Envelope})
	if err == nil {
		t.Fatal("reused a consumed prekey")
	}
	raw, _ := json.Marshal(send)
	if strings.Contains(string(raw), "hello") {
		t.Fatal("plaintext in encrypted response")
	}
}

func TestInputLimits(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	for _, r := range []Request{{Op: NewAccount}, {Op: NewAccount, Key: make([]byte, 32), Count: 101}, {Op: NewAccount, Key: make([]byte, 32), Count: 0}, {Op: "unsupported", Key: make([]byte, 32)}} {
		if _, err := e.Apply(ctx, r); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
}

func TestProtectedSnapshotsAndFailureIsolation(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	key := make([]byte, 32)
	a, err := e.Apply(ctx, Request{Op: NewAccount, Key: key, Count: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Apply(ctx, Request{Op: PreKeys, Key: key, Account: a.Account, Count: 1}); err == nil {
		t.Fatal("evicted unconsumed prekeys")
	}
	wrong := make([]byte, 32)
	wrong[0] = 1
	if _, err = e.Apply(ctx, Request{Op: PreKeys, Key: wrong, Account: a.Account, Count: 1}); err == nil {
		t.Fatal("accepted wrong storage key")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if out, err := e.Apply(cancelled, Request{Op: NewAccount, Key: key, Count: 1}); err == nil || out.Account != "" {
		t.Fatal("cancelled operation returned state")
	}
	e.entropy = func([]byte) (int, error) { return 0, errors.New("test-only entropy outage") }
	if out, err := e.Apply(ctx, Request{Op: NewAccount, Key: key, Count: 1}); err == nil || out.Account != "" {
		t.Fatal("entropy failure returned state")
	}
	e.entropy = rand.Read
	if _, err = e.Apply(ctx, Request{Op: NewAccount, Key: key, Count: 1}); err != nil {
		t.Fatal("failed operation poisoned core")
	}
}

func TestDeterministicInteropVector(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	// Deterministic entropy is confined to this test, never exported by the API.
	var counter uint64
	e.entropy = func(p []byte) (int, error) {
		n := len(p)
		for len(p) > 0 {
			var seed [8]byte
			binary.BigEndian.PutUint64(seed[:], counter)
			counter++
			hash := sha256.Sum256(seed[:])
			used := copy(p, hash[:])
			p = p[used:]
		}
		return n, nil
	}
	key := make([]byte, 32)
	key[0] = 42
	call := func(r Request) Response {
		t.Helper()
		r.Key = key
		out, err := e.Apply(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := call(Request{Op: NewAccount, Count: 1})
	b := call(Request{Op: NewAccount, Count: 1})
	binding := Binding{Server: "vector-server", Conversation: "vector-conversation", Sender: "alice", SenderDevice: "desktop", Recipient: "bob", RecipientDevice: "browser", MessageID: "one", Purpose: "message"}
	send := call(Request{Op: Outbound, Account: a.Account, PeerIdentity: b.Bundle.Identity, OneTimeKey: b.Bundle.OneTimeKeys[0].Key, Binding: &binding, Plaintext: "deterministic native/browser vector"})
	inbound := Request{Op: Inbound, Key: key, Account: b.Account, PeerIdentity: a.Bundle.Identity, Binding: &binding, Envelope: send.Envelope}
	received := call(inbound)
	binding.Sender, binding.Recipient = binding.Recipient, binding.Sender
	binding.SenderDevice, binding.RecipientDevice = binding.RecipientDevice, binding.SenderDevice
	binding.MessageID = "two"
	reply := call(Request{Op: Encrypt, Session: received.Session, Binding: &binding, Plaintext: "deterministic browser/native reply"})
	decrypt := Request{Op: Decrypt, Key: key, Session: send.Session, Binding: &binding, Envelope: reply.Envelope}
	// Copy bindings: requests retain the exact independently expected direction.
	initialBinding := send.Envelope.Binding
	inbound.Binding = &initialBinding
	vector := struct {
		Inbound Request `json:"inbound"`
		Decrypt Request `json:"decrypt"`
		First   string  `json:"first_plaintext"`
		Reply   string  `json:"reply_plaintext"`
	}{inbound, decrypt, received.Plaintext, "deterministic browser/native reply"}
	encoded, err := json.MarshalIndent(vector, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	const file = "testdata/interop-vector.json"
	if os.Getenv("NOXA_UPDATE_RATCHET_VECTORS") == "1" {
		if err := os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, expected) {
		t.Fatal("deterministic protocol vector changed; inspect before explicitly regenerating")
	}
	if got := call(decrypt); got.Plaintext != vector.Reply {
		t.Fatal("reply mismatch")
	}
}

func TestRuntimeMemoryAndWorkBounds(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	m, err := e.runtime.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close(ctx) }()
	if _, ok := m.Memory().Grow(1024); ok {
		t.Fatal("WASM exceeded 64 MiB cap")
	}
	e.slots <- struct{}{}
	e.slots <- struct{}{}
	if _, err := e.Apply(ctx, Request{Op: NewAccount, Key: make([]byte, 32), Count: 1}); err == nil {
		t.Fatal("exceeded concurrent operation cap")
	}
	<-e.slots
	<-e.slots
}
