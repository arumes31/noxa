package framecrypt

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type vector struct {
	Key        string `json:"key"`
	Kid        uint8  `json:"kid"`
	SSRC       uint32 `json:"ssrc"`
	TS         uint32 `json:"ts"`
	Seed       uint64 `json:"seed"`
	Index      int    `json:"index"`
	Plaintext  string `json:"plaintext"`
	Ciphertext string `json:"ciphertext"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decryptH264 mirrors the receiving LiveKit worker, for round-trip checks.
func decryptH264(t *testing.T, material, frame []byte) []byte {
	t.Helper()
	clearBytes, err := unencryptedBytes(frame)
	if err != nil {
		t.Fatal(err)
	}
	var tail []byte
	for i, b := range frame[clearBytes:] {
		rest := frame[clearBytes:]
		if i >= 2 && b == 3 && rest[i-1] == 0 && rest[i-2] == 0 && i+1 < len(rest) && rest[i+1] <= 3 {
			continue
		}
		tail = append(tail, b)
	}
	if len(tail) < 30 || tail[len(tail)-2] != ivLength {
		t.Fatal("missing trailer")
	}
	iv := tail[len(tail)-14 : len(tail)-2]
	aead, err := deriveAEAD(material)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, iv, tail[:len(tail)-14], frame[:clearBytes])
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte(nil), frame[:clearBytes]...), plain...)
}

func TestLiveKitVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/livekit-h264-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Vectors []vector }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Vectors) < 8 {
		t.Fatalf("only %d vectors", len(file.Vectors))
	}
	escaped := false
	var enc *Encryptor
	for i, v := range file.Vectors {
		key := unhex(t, v.Key)
		if v.Index == 0 { // A new LiveKit cryptor: fresh send count.
			enc = NewEncryptor()
			seed := v.Seed
			enc.seed = func() uint64 { return seed }
			if err := enc.SetKey(1, v.Kid, key); err != nil {
				t.Fatal(err)
			}
			if err := enc.Activate(1); err != nil {
				t.Fatal(err)
			}
		}
		plain, want := unhex(t, v.Plaintext), unhex(t, v.Ciphertext)
		got, err := enc.EncryptH264(plain, v.SSRC, v.TS)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("vector %d differs from LiveKit\n got %x\nwant %x", i, got, want)
		}
		// Round trip: the header comes back normalized, the slice tail original.
		c := mustClear(t, plain)
		if !bytes.Equal(decryptH264(t, key, got), append(normalizeAnnexBStartCodes(plain[:c]), plain[c:]...)) {
			t.Fatalf("vector %d: round trip mismatch", i)
		}
		if bytes.Count(plain, []byte{0, 0, 1}) == bytes.Count(plain, []byte{0, 0, 0, 1}) && len(got) > len(plain)+30 {
			escaped = true
		}
	}
	if !escaped {
		t.Fatal("no vector exercises emulation prevention")
	}
}

func mustClear(t *testing.T, frame []byte) int {
	t.Helper()
	n, err := unencryptedBytes(frame)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEncryptorFailsClosed(t *testing.T) {
	enc := NewEncryptor()
	frame := []byte{0, 0, 0, 1, 0x41, 0x9a, 1, 2, 3}
	if out, err := enc.EncryptH264(frame, 1, 1); !errors.Is(err, ErrNoKey) || out != nil {
		t.Fatalf("no key: %x %v", out, err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	if err := enc.SetKey(5, 1, key); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.EncryptH264(frame, 1, 1); !errors.Is(err, ErrNoKey) {
		t.Fatal("installed but inactive key encrypted")
	}
	if err := enc.Activate(5); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.EncryptH264(frame, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.EncryptH264([]byte{0, 0, 0, 1, 0x67, 1, 2, 3}, 1, 2); err == nil {
		t.Fatal("frame without slice encrypted")
	}
	enc.RetireOne(5)
	if _, err := enc.EncryptH264(frame, 1, 3); !errors.Is(err, ErrNoKey) {
		t.Fatal("retired active key still encrypts")
	}
}

func TestEncryptorEpochRules(t *testing.T) {
	enc := NewEncryptor()
	k := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	for _, bad := range []error{enc.SetKey(0, 0, k(1)), enc.SetKey(1, 16, k(1)), enc.SetKey(1, 0, k(1)[:31]), enc.Activate(9)} {
		if bad == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if err := enc.SetKey(10, 2, k(1)); err != nil {
		t.Fatal(err)
	}
	if enc.SetKey(10, 2, k(1)) != nil || enc.SetKey(10, 2, k(2)) == nil || enc.SetKey(10, 3, k(1)) == nil {
		t.Fatal("idempotent/conflicting re-set handled wrongly")
	}
	if enc.SetKey(9, 2, k(3)) == nil {
		t.Fatal("older epoch took a newer epoch's key slot")
	}
	_ = enc.Activate(10)
	if err := enc.SetKey(11, 2, k(4)); err != nil { // Same slot: epoch 10 retires.
		t.Fatal(err)
	}
	if _, ok := enc.Active(); ok {
		t.Fatal("evicted epoch still active")
	}
	if enc.SetKey(10, 5, k(1)) == nil {
		t.Fatal("retired epoch reinstalled")
	}
	for e := uint64(12); e <= 15; e++ {
		if err := enc.SetKey(e, uint8(e%16), k(byte(e))); err != nil {
			t.Fatal(err)
		}
	}
	if len(enc.epochs) != maxEpochs || enc.Activate(11) == nil {
		t.Fatalf("live epochs = %d; oldest not dropped", len(enc.epochs))
	}
	_ = enc.Activate(14)
	if enc.Activate(13) == nil {
		t.Fatal("activated an older epoch")
	}
	enc.Retire(14)
	if _, ok := enc.Active(); ok || len(enc.epochs) != 1 {
		t.Fatal("retire left epochs")
	}
	enc.Forget()
	if len(enc.epochs) != 0 || enc.SetKey(15, 0, k(9)) == nil {
		t.Fatal("forget allowed a retired epoch back")
	}
}

func TestCanonicalH264(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f}
	pps := []byte{0x68, 0x87, 0xcb}
	idr := []byte{0x65, 0x88, 0x84, 1}
	p := []byte{0x41, 0x9a, 2}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	sc3, sc4 := []byte{0, 0, 1}, []byte{0, 0, 0, 1}
	want := join(sc4, sps, sc4, pps, sc4, idr)

	// AUD, SEI, prefix and filler dropped; 3-byte codes and SPS trailing zeros normalized.
	in := join(sc3, []byte{0x09, 0xf0}, sc3, []byte{0x06, 5, 1, 0x80}, sc4, sps, []byte{0, 0}, sc3, pps,
		sc3, []byte{0x6e, 0xc0, 0x80, 0x0f, 0x07}, sc3, idr, sc3, []byte{0x0c, 0xff, 0xff})
	if out, key, err := CanonicalH264(in, nil, nil); err != nil || !key || !bytes.Equal(out, want) {
		t.Fatalf("keyframe: %x %v %v", out, key, err)
	}
	// Missing SPS/PPS inserted from the sequence header.
	if out, key, err := CanonicalH264(join(sc4, idr), sps, pps); err != nil || !key || !bytes.Equal(out, want) {
		t.Fatalf("inserted: %x %v %v", out, key, err)
	}
	// Delta frames carry only the slice.
	if out, key, err := CanonicalH264(join(sc4, sps, sc4, pps, sc3, p), sps, pps); err != nil || key || !bytes.Equal(out, join(sc4, p)) {
		t.Fatalf("delta: %x %v %v", out, key, err)
	}
	for name, bad := range map[string][]byte{
		"no slice":      join(sc4, sps, sc4, pps),
		"two slices":    join(sc4, p, sc4, p),
		"partition":     join(sc4, []byte{0x42, 1}),
		"leading data":  join([]byte{1}, sc4, p),
		"not annex-b":   p,
		"forbidden bit": join(sc4, []byte{0xc1, 1}),
		"two sps":       join(sc4, sps, sc4, sps, sc4, pps, sc4, idr),
	} {
		if _, _, err := CanonicalH264(bad, sps, pps); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, _, err := CanonicalH264(join(sc4, idr), nil, nil); err == nil {
		t.Error("keyframe without SPS/PPS accepted")
	}
}
