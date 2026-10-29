package admission

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPayloadApplicationJSON(t *testing.T) {
	m := Manifest{}
	target := Target{}
	for _, body := range []string{`{"mentions":{"User-1":1,"é":2}}`, strings.Repeat("[", 20) + "0" + strings.Repeat("]", 20), `{"key":1,"key":2}`} {
		plain, e := SealPayload(m, target, json.RawMessage(body))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = OpenPayload(m, target, plain); e != nil {
			t.Fatalf("application JSON rejected: %s: %v", body, e)
		}
	}
	plain, _ := SealPayload(m, target, json.RawMessage(`null`))
	for _, bad := range []string{strings.Replace(plain, `"context":`, `"body":0,"context":`, 1), strings.Replace(plain, `"version":`, `"version":"bad","version":`, 1), strings.Replace(plain, `"version":`, `"unknown":0,"version":`, 1)} {
		if _, e := OpenPayload(m, target, bad); e == nil {
			t.Fatal("ambiguous/unknown context accepted")
		}
	}
}

func TestSharedUnicodeBindingVectors(t *testing.T) {
	b, e := os.ReadFile("testdata/binding-unicode.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []struct{ Sender, Peer, Conversation string }
	if e = json.Unmarshal(b, &vectors); e != nil {
		t.Fatal(e)
	}
	for _, v := range vectors {
		if got := ExpectedBinding(Manifest{Scope: "direct", Sender: v.Sender, Peer: v.Peer}, Target{}).Conversation; got != v.Conversation {
			t.Fatalf("%q != %q", got, v.Conversation)
		}
	}
}
