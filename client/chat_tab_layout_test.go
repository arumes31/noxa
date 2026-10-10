package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func chatTabLayoutTestApp(t *testing.T) (*App, *connManager, DMHistoryContext) {
	t.Helper()
	a, cm := dmContextTestApp(t)
	cm.mu.Lock()
	cm.addr, cm.fingerprint, cm.tlsUsed = "Example.COM:12333", "certificate-a", true
	cm.mu.Unlock()
	owner, err := a.DMHistoryContextForTab("a")
	if err != nil {
		t.Fatal(err)
	}
	return a, cm, owner
}

func TestChatTabLayoutEncryptedRoundTrip(t *testing.T) {
	a, _, owner := chatTabLayoutTestApp(t)
	empty, err := a.ChatTabLayoutForContext(owner)
	if err != nil || empty.Order == nil || empty.Pinned == nil || len(empty.Order)+len(empty.Pinned) != 0 {
		t.Fatalf("missing file: %+v / %v", empty, err)
	}
	want := ChatTabLayout{Order: []string{"dm:private-peer-canary", "ch:21", "dm:other"}, Pinned: []string{"ch:21"}}
	if err := a.SaveChatTabLayoutForContext(owner, want); err != "" {
		t.Fatal(err)
	}
	store, err := a.chatTabLayoutStore(owner)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-peer-canary", "ch:21", "example.com", "certificate-a", `"order"`} {
		if bytes.Contains(blob, []byte(secret)) || strings.Contains(filepath.Base(store.path), secret) {
			t.Fatalf("layout exposed plaintext %q", secret)
		}
	}
	got, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("roundtrip: %+v / %v", got, err)
	}
	got.Order[0] = "dm:caller-mutation"
	again, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("caller changed persisted layout: %+v / %v", again, err)
	}
}

func TestChatTabLayoutPinnedNamesEncryptedRoundTrip(t *testing.T) {
	a, _, owner := chatTabLayoutTestApp(t)
	want := ChatTabLayout{
		Order: []string{"dm:empty-peer", "ch:21"}, Pinned: []string{"dm:empty-peer", "ch:21"},
		Names: map[string]string{"dm:empty-peer": "Änne <private-name-canary>"},
	}
	input := want
	input.Names = map[string]string{"dm:empty-peer": "  " + want.Names["dm:empty-peer"] + "  "}
	if err := a.SaveChatTabLayoutForContext(owner, input); err != "" {
		t.Fatal(err)
	}
	if input.Names["dm:empty-peer"] != "  "+want.Names["dm:empty-peer"]+"  " {
		t.Fatal("normalization mutated the caller's map")
	}
	store, err := a.chatTabLayoutStore(owner)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("private-name-canary")) {
		t.Fatal("pinned name was stored in plaintext")
	}
	got, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("name roundtrip: %+v / %v", got, err)
	}
	got.Names["dm:empty-peer"] = "caller mutation"
	again, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("caller changed persisted names: %+v / %v", again, err)
	}
	for _, empty := range []ChatTabLayout{{}, {Names: map[string]string{}}} {
		raw, err := json.Marshal(empty)
		if err != nil || bytes.Contains(raw, []byte(`"names"`)) {
			t.Fatalf("empty name metadata should be optional: %s / %v", raw, err)
		}
	}
}

func TestChatTabLayoutPinnedNameValidation(t *testing.T) {
	a, _, owner := chatTabLayoutTestApp(t)
	base := ChatTabLayout{Order: []string{"dm:pinned", "dm:unpinned", "ch:1"}, Pinned: []string{"dm:pinned", "ch:1"}}
	for _, key := range []string{"dm:unpinned", "dm:unknown", "ch:1", ""} {
		t.Run("key/"+key, func(t *testing.T) {
			layout := base
			layout.Names = map[string]string{key: "Name"}
			if err := a.SaveChatTabLayoutForContext(owner, layout); err == "" {
				t.Fatal("name without a pinned DM reference accepted")
			}
		})
	}
	for _, name := range []string{"", "   ", "line\nnext", "line\rnext", "name\t", "name\x00", "name\u2028next", "name\u2029next", string([]byte{0xff}), strings.Repeat("a", 65), strings.Repeat("😀", 65)} {
		t.Run("name/"+name, func(t *testing.T) {
			layout := base
			layout.Names = map[string]string{"dm:pinned": name}
			if err := a.SaveChatTabLayoutForContext(owner, layout); err == "" {
				t.Fatal("invalid display name accepted")
			}
		})
	}
	maximum := ChatTabLayout{Names: make(map[string]string, 64)}
	for i := range 64 {
		key := fmt.Sprintf("dm:peer%d", i)
		maximum.Order = append(maximum.Order, key)
		maximum.Pinned = append(maximum.Pinned, key)
		maximum.Names[key] = strings.Repeat("😀", 64)
	}
	if err := a.SaveChatTabLayoutForContext(owner, maximum); err != "" {
		t.Fatalf("maximum valid names: %s", err)
	}
	got, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(got, maximum) {
		t.Fatalf("maximum name roundtrip failed: %v", err)
	}
	maximum.Names["dm:extra"] = "Extra"
	if err := a.SaveChatTabLayoutForContext(owner, maximum); err == "" {
		t.Fatal("too many names accepted")
	}
}

func TestChatTabLayoutScopesIdentityAddressAndCertificate(t *testing.T) {
	for _, change := range []string{"identity", "address", "certificate"} {
		t.Run(change, func(t *testing.T) {
			a, cm, owner := chatTabLayoutTestApp(t)
			layout := ChatTabLayout{
				Order: []string{"dm:private-peer"}, Pinned: []string{"dm:private-peer"},
				Names: map[string]string{"dm:private-peer": "Private name"},
			}
			if err := a.SaveChatTabLayoutForContext(owner, layout); err != "" {
				t.Fatal(err)
			}
			first, err := a.chatTabLayoutStore(owner)
			if err != nil {
				t.Fatal(err)
			}
			blob, err := os.ReadFile(first.path)
			if err != nil {
				t.Fatal(err)
			}
			if change == "identity" {
				a.identityMu.Lock()
				replaceDMIdentityForTest(a, mustTempIdentity(t))
				a.identityMu.Unlock()
			} else {
				cm.mu.Lock()
				if change == "address" {
					cm.addr = "other.example:12333"
				} else {
					cm.fingerprint = "certificate-b"
				}
				cm.mu.Unlock()
			}
			fresh, err := a.DMHistoryContextForTab("a")
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.ChatTabLayoutForContext(fresh)
			if err != nil || len(got.Order)+len(got.Pinned)+len(got.Names) != 0 {
				t.Fatalf("other scope inherited layout: %+v / %v", got, err)
			}
			second, err := a.chatTabLayoutStore(fresh)
			if err != nil || first.path == second.path || first.key == second.key {
				t.Fatalf("scope did not separate file and key: %v", err)
			}
			if err := os.WriteFile(second.path, blob, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ChatTabLayoutForContext(fresh); err == nil {
				t.Fatal("ciphertext copied from another scope was accepted")
			}
			if err := a.SaveChatTabLayoutForContext(fresh, ChatTabLayout{}); err == "" {
				t.Fatal("save silently overwrote unreadable layout")
			}
			after, err := os.ReadFile(second.path)
			if err != nil || !bytes.Equal(blob, after) {
				t.Fatalf("unreadable layout was changed: %v", err)
			}
		})
	}
}

func TestChatTabLayoutRejectsStaleContext(t *testing.T) {
	for _, change := range []string{"tab", "reactivation", "identity", "empty"} {
		t.Run(change, func(t *testing.T) {
			a, _, owner := chatTabLayoutTestApp(t)
			layout := ChatTabLayout{Order: []string{"ch:1"}, Pinned: []string{}}
			if err := a.SaveChatTabLayoutForContext(owner, layout); err != "" {
				t.Fatal(err)
			}
			switch change {
			case "tab", "reactivation":
				a.tabsMu.Lock()
				a.activateLocked("")
				if change == "reactivation" {
					a.activateLocked("a")
				}
				a.tabsMu.Unlock()
			case "identity":
				a.identityMu.Lock()
				replaceDMIdentityForTest(a, mustTempIdentity(t))
				a.identityMu.Unlock()
			case "empty":
				owner = DMHistoryContext{}
			}
			if _, err := a.ChatTabLayoutForContext(owner); err == nil {
				t.Fatal("stale read accepted")
			}
			if err := a.SaveChatTabLayoutForContext(owner, ChatTabLayout{}); err == "" {
				t.Fatal("stale write accepted")
			}
		})
	}
}

func TestChatTabLayoutValidation(t *testing.T) {
	a, _, owner := chatTabLayoutTestApp(t)
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("ch:%d", i+1)
	}
	for _, tc := range []struct {
		name   string
		layout ChatTabLayout
	}{
		{"order limit", ChatTabLayout{Order: tooMany}},
		{"pin limit", ChatTabLayout{Order: tooMany[:65], Pinned: tooMany[:65]}},
		{"duplicate order", ChatTabLayout{Order: []string{"ch:1", "ch:1"}}},
		{"duplicate pin", ChatTabLayout{Order: []string{"ch:1"}, Pinned: []string{"ch:1", "ch:1"}}},
		{"missing pin", ChatTabLayout{Order: []string{"ch:1"}, Pinned: []string{"dm:peer"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.SaveChatTabLayoutForContext(owner, tc.layout); err == "" {
				t.Fatal("invalid layout accepted")
			}
		})
	}
	for _, key := range []string{"", "ch:0", "ch:-1", "ch:+1", "ch:01", "ch:9223372036854775808", "dm:", "dm:has space", "dm:line\n", "dm:" + strings.Repeat("x", 129), "other:1"} {
		t.Run(key, func(t *testing.T) {
			if err := a.SaveChatTabLayoutForContext(owner, ChatTabLayout{Order: []string{key}}); err == "" {
				t.Fatal("invalid key accepted")
			}
		})
	}
	maximum := ChatTabLayout{Order: tooMany[:256], Pinned: tooMany[:64]}
	if err := a.SaveChatTabLayoutForContext(owner, maximum); err != "" {
		t.Fatalf("maximum valid layout: %s", err)
	}
}

func TestChatTabLayoutBoundsPreservePreviousFile(t *testing.T) {
	a, _, owner := chatTabLayoutTestApp(t)
	want := ChatTabLayout{Order: []string{"ch:1"}, Pinned: []string{}}
	if err := a.SaveChatTabLayoutForContext(owner, want); err != "" {
		t.Fatal(err)
	}
	// JSON escaping can expand a valid 128-byte reference far beyond its input
	// size. A successful write must always fit the bounded decoder on restart.
	large := ChatTabLayout{Order: make([]string, 256)}
	for i := range large.Order {
		large.Order[i] = fmt.Sprintf("dm:%03d%s", i, strings.Repeat("<", 125))
	}
	if !large.valid() {
		t.Fatal("fixture does not reach serialization size validation")
	}
	if err := a.SaveChatTabLayoutForContext(owner, large); err == "" {
		t.Fatal("oversized serialized layout accepted")
	}
	got, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected write changed previous layout: %+v / %v", got, err)
	}
}

func TestChatTabLayoutRejectsDamagedAndOversizedStorage(t *testing.T) {
	for _, damage := range []string{"ciphertext", "oversized", "invalid JSON", "invalid layout", "unpinned name"} {
		t.Run(damage, func(t *testing.T) {
			a, _, owner := chatTabLayoutTestApp(t)
			store, err := a.chatTabLayoutStore(owner)
			if err != nil {
				t.Fatal(err)
			}
			var blob []byte
			switch damage {
			case "ciphertext":
				blob = []byte("invalid ciphertext")
			case "oversized":
				blob = make([]byte, sealedAttachmentSizeForPlaintext(chatTabLayoutMaxBytes)+1)
			case "invalid JSON":
				blob, err = sealFile([]byte("{"), store.key)
			case "invalid layout":
				blob, err = sealFile([]byte(`{"order":["ch:0"],"pinned":[]}`), store.key)
			case "unpinned name":
				blob, err = sealFile([]byte(`{"order":["dm:peer"],"pinned":[],"names":{"dm:peer":"Name"}}`), store.key)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := writePrivateFileAtomic(store.path, blob); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ChatTabLayoutForContext(owner); err == nil {
				t.Fatal("damaged layout loaded")
			}
			if err := a.SaveChatTabLayoutForContext(owner, ChatTabLayout{}); err == "" {
				t.Fatal("damaged layout overwritten")
			}
			after, err := os.ReadFile(store.path)
			if err != nil || !bytes.Equal(after, blob) {
				t.Fatalf("failed save changed damaged data: %v", err)
			}
		})
	}
}

func TestChatTabLayoutCanonicalServerAddress(t *testing.T) {
	a, cm, owner := chatTabLayoutTestApp(t)
	want := ChatTabLayout{Order: []string{"dm:peer"}, Pinned: []string{}}
	if err := a.SaveChatTabLayoutForContext(owner, want); err != "" {
		t.Fatal(err)
	}
	cm.mu.Lock()
	cm.addr = "example.com.:12333"
	cm.mu.Unlock()
	got, err := a.ChatTabLayoutForContext(owner)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("address alias changed the persisted scope: %+v / %v", got, err)
	}
}

func TestChatTabLayoutRequiresCurrentServerMetadata(t *testing.T) {
	for _, state := range []string{"disconnected", "closed", "empty address", "invalid address", "unknown certificate"} {
		t.Run(state, func(t *testing.T) {
			a, cm, owner := chatTabLayoutTestApp(t)
			cm.mu.Lock()
			switch state {
			case "disconnected":
				cm.conn = nil
			case "closed":
				cm.closed = true
			case "empty address":
				cm.addr = ""
			case "invalid address":
				cm.addr = "not-an-address"
			case "unknown certificate":
				cm.fingerprint = ""
			}
			cm.mu.Unlock()
			if _, err := a.ChatTabLayoutForContext(owner); err == nil {
				t.Fatal("read accepted missing server scope")
			}
			if err := a.SaveChatTabLayoutForContext(owner, ChatTabLayout{}); err == "" {
				t.Fatal("write accepted missing server scope")
			}
		})
	}
}
