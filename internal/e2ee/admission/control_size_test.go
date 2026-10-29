package admission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/e2ee/ratchet"
)

// The largest control a client can produce: every text field at the 128-byte
// ValidText limit (the two principals at 120, so the direct conversation binding
// stays within the core's 256-byte field limit), control data at MaxControlData, sealed as a session-opening
// (pre-key) Olm message, the biggest Olm form. It must fit the server-side
// control envelope cap.
func TestMaxControlEnvelopeFitsCap(t *testing.T) {
	ctx := context.Background()
	e, err := ratchet.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	key := make([]byte, 32)
	call := func(r ratchet.Request) ratchet.Response {
		t.Helper()
		r.Key = key
		out, err := e.Apply(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	text := func(c string) string { return strings.Repeat(c, 128) }
	m := Manifest{Version: Version, Server: text("s"), Sender: strings.Repeat("a", 120), Device: text("d"), AccountEpoch: 9007199254740991,
		AttemptID: "9007199254740991", LogicalID: text("l"), Purpose: "control", Scope: "direct", Peer: strings.Repeat("b", 120), Deadline: 9007199254740991}
	target := Target{Principal: m.Peer, Device: text("t"), AccountEpoch: 9007199254740991}
	data, _ := json.Marshal(map[string]string{"x": strings.Repeat("k", MaxControlData-8)})
	if len(data) != MaxControlData {
		t.Fatalf("data %d", len(data))
	}
	body, _ := json.Marshal(ControlBody{Kind: strings.Repeat("k", 64), Scope: ControlScope{Type: strings.Repeat("c", 64), ID: text("i")}, Epoch: MaxControlEpoch, ExpiresAt: 9007199254740991, Data: data})
	if _, err := ParseControlBody(body); err != nil {
		t.Fatal("not a valid control body", err)
	}
	plaintext, err := SealPayload(m, target, body)
	if err != nil {
		t.Fatal(err)
	}
	a := call(ratchet.Request{Op: ratchet.NewAccount, Count: 1})
	b := call(ratchet.Request{Op: ratchet.NewAccount, Count: 1})
	binding := ExpectedBinding(m, target)
	send := call(ratchet.Request{Op: ratchet.Outbound, Account: a.Account, PeerIdentity: b.Bundle.Identity, OneTimeKey: b.Bundle.OneTimeKeys[0].Key, Binding: &binding, Plaintext: plaintext})
	envelope, _ := json.Marshal(send.Envelope)
	t.Logf("largest control: plaintext %d bytes, envelope %d bytes (cap %d)", len(plaintext), len(envelope), MaxControlEnvelopeBytes)
	if len(envelope) > MaxControlEnvelopeBytes {
		t.Fatalf("largest valid control envelope %d exceeds the cap %d", len(envelope), MaxControlEnvelopeBytes)
	}
	// A real channel media-key control (media-keys.js buildMediaKeyBody: four
	// slot keys, a membership digest, realistic identifiers), also pre-key.
	real := Manifest{Version: Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: strings.Repeat("u", 44), Device: "d-" + strings.Repeat("0", 36),
		AccountEpoch: 1, AttemptID: "123456789", LogicalID: "ctl-" + strings.Repeat("0", 36), Purpose: "control", Scope: "direct", Peer: strings.Repeat("p", 44), Deadline: 1893456000}
	realTarget := Target{Principal: real.Peer, Device: "d-" + strings.Repeat("1", 36), AccountEpoch: 1}
	key44 := strings.Repeat("K", 43) + "="
	mediaData, _ := json.Marshal(map[string]any{"v": 1, "client": strings.Repeat("c", 36), "session": strings.Repeat("s", 36), "kid": 15,
		"members": strings.Repeat("m", 43), "n": 64, "keys": map[string]string{"mic": key44, "cam": key44, "screen": key44, "screenaudio": key44}})
	mediaBody, _ := json.Marshal(ControlBody{Kind: "media-key", Scope: ControlScope{Type: "channel-media", ID: "1234567"}, Epoch: 1893456000123, ExpiresAt: 1893456120, Data: mediaData})
	mediaPlain, _ := SealPayload(real, realTarget, mediaBody)
	realBinding := ExpectedBinding(real, realTarget)
	c := call(ratchet.Request{Op: ratchet.NewAccount, Count: 1})
	media := call(ratchet.Request{Op: ratchet.Outbound, Account: a.Account, PeerIdentity: c.Bundle.Identity, OneTimeKey: c.Bundle.OneTimeKeys[0].Key, Binding: &realBinding, Plaintext: mediaPlain})
	mediaEnvelope, _ := json.Marshal(media.Envelope)
	t.Logf("real media-key control: plaintext %d bytes, envelope %d bytes", len(mediaPlain), len(mediaEnvelope))
	if len(mediaEnvelope) > MaxControlEnvelopeBytes/2 {
		t.Fatalf("a real media-key control (%d) should be far below the cap", len(mediaEnvelope))
	}
}
