package netproto

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"strings"
	"testing"
)

// The deterministic vector mobile/unit/fixtures/history-envelope.txt is
// decoded and verified by mobile/unit/history-envelope.test.mjs (Go<->JS).
func TestHistoryEnvelopeVectorAndRefusals(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	records := []byte(`[{"peer":"bob","from":"bob","body":"hi","sent_at":5,"client_msg_id":"R1","protocol":"noxa-olm-v1","provenance":"authenticated"}]`)
	plain, err := EncodeHistoryChunk(HistoryHeader{Source: "desk", Target: "phone", Transfer: "t1", Seq: 0, Total: 1}, records,
		func(b []byte) ([]byte, error) { return ed25519.Sign(key, b), nil })
	if err != nil {
		t.Fatal(err)
	}
	const path = "../../mobile/unit/fixtures/history-envelope.txt"
	if os.Getenv("NOXA_UPDATE_HISTORY_VECTOR") != "" {
		_ = os.WriteFile(path, plain, 0o644)
	}
	if want, err := os.ReadFile(path); err != nil || !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), plain) {
		t.Fatalf("history envelope vector changed: %v", err)
	}
	h, got, err := DecodeHistoryChunk(plain)
	tr, _ := HistoryChunkTranscript(h)
	if err != nil || !bytes.Equal(got, records) || !ed25519.Verify(key.Public().(ed25519.PublicKey), tr, h.Signature) {
		t.Fatalf("decode: %v", err)
	}
	for _, bad := range [][]byte{records, append([]byte(nil), plain[:len(plain)-2]...), []byte(strings.Replace(string(plain), `"seq":0`, `"seq":1`, 1))} {
		if _, _, err := DecodeHistoryChunk(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if !IsHistoryEnvelope(string(plain)) || IsHistoryEnvelope(string(records)) {
		t.Fatal("IsHistoryEnvelope")
	}
}
